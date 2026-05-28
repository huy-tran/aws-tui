package s3

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/loader"
)

const headProbeConcurrency = 8

type uploadStage int

const (
	stagePicking uploadStage = iota
	stageProbing
	stageConfirming
	stageUploading
	stageDone
)

type (
	headProbeDoneMsg struct{ exists map[string]bool }
	uploadStepMsg    struct {
		index int
		path  string
		key   string
		err   error
	}
	uploadAllDoneMsg struct{}
	refreshPrefixMsg struct{}
)

type uploadItem struct {
	path string
	key  string // destination key under the bucket
	size int64
}

type uploadModel struct {
	ctx    *awspkg.Context
	region string
	bucket string
	prefix string

	picker  filepicker.Model
	stage   uploadStage
	queue   []uploadItem
	exists  map[string]bool // key -> true if HeadObject succeeded
	results []uploadStepMsg
	stepIdx int

	loader loader.Model
	err    error
	status string
}

func newUpload(ctx *awspkg.Context, region, bucket, prefix string) uploadModel {
	fp := filepicker.New()
	fp.AllowedTypes = nil
	fp.ShowHidden = false
	fp.ShowPermissions = false
	fp.ShowSize = true
	fp.AutoHeight = false
	fp.SetHeight(15)
	if home, err := os.UserHomeDir(); err == nil {
		fp.CurrentDirectory = home
	}
	// Drop esc from Back so esc cancels the whole modal.
	fp.KeyMap.Back = key.NewBinding(key.WithKeys("h", "backspace", "left"), key.WithHelp("h", "back"))

	return uploadModel{
		ctx:    ctx,
		region: region,
		bucket: bucket,
		prefix: prefix,
		picker: fp,
		loader: loader.New(),
	}
}

func (m uploadModel) Init() tea.Cmd {
	return m.picker.Init()
}

func (m uploadModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h := msg.Height - 10
		if h < 5 {
			h = 5
		}
		m.picker.SetHeight(h)
		return m, nil

	case headProbeDoneMsg:
		m.exists = msg.exists
		hasCollision := false
		for _, ok := range msg.exists {
			if ok {
				hasCollision = true
				break
			}
		}
		if hasCollision {
			m.stage = stageConfirming
			return m, nil
		}
		m.stage = stageUploading
		return m, tea.Batch(m.loader.Tick(), m.uploadStepCmd(0))

	case uploadStepMsg:
		m.results = append(m.results, msg)
		next := msg.index + 1
		if next >= len(m.queue) {
			return m, func() tea.Msg { return uploadAllDoneMsg{} }
		}
		m.stepIdx = next
		return m, m.uploadStepCmd(next)

	case uploadAllDoneMsg:
		m.stage = stageDone
		m.ctx.Cache.Invalidate("s3:objects:" + m.bucket + ":" + m.prefix)
		return m, nil

	case spinner.TickMsg:
		if m.stage != stageProbing && m.stage != stageUploading {
			return m, nil
		}
		var cmd tea.Cmd
		m.loader, cmd = m.loader.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.updateKeys(msg)
	}
	return m, nil
}

func (m uploadModel) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.stage {
	case stagePicking:
		if msg.String() == "esc" {
			return m, nav.PopView()
		}
		if msg.String() == "U" {
			if len(m.queue) == 0 {
				m.status = "queue is empty - select files with enter first"
				return m, nil
			}
			m.stage = stageProbing
			m.status = ""
			return m, tea.Batch(m.loader.Tick(), m.headProbeCmd())
		}
		// Let filepicker handle navigation; intercept selections to toggle queue.
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(msg)
		if picked, path := m.picker.DidSelectFile(msg); picked {
			m.toggleQueue(path)
			m.status = ""
		}
		return m, cmd

	case stageConfirming:
		switch msg.String() {
		case "y":
			m.stage = stageUploading
			return m, tea.Batch(m.loader.Tick(), m.uploadStepCmd(0))
		case "n", "esc":
			m.stage = stagePicking
			return m, nil
		}
		return m, nil

	case stageProbing, stageUploading:
		// Block input during async work; allow esc only after done.
		return m, nil

	case stageDone:
		if msg.String() == "esc" || msg.String() == "enter" {
			return m, tea.Batch(nav.PopView(), func() tea.Msg { return refreshPrefixMsg{} })
		}
		return m, nil
	}
	return m, nil
}

func (m *uploadModel) toggleQueue(path string) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	dstKey := m.prefix + filepath.Base(path)
	for i, it := range m.queue {
		if it.path == path {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			return
		}
	}
	m.queue = append(m.queue, uploadItem{path: path, key: dstKey, size: info.Size()})
}

func (m uploadModel) headProbeCmd() tea.Cmd {
	ctx := m.ctx
	region := m.region
	bucket := m.bucket
	items := make([]uploadItem, len(m.queue))
	copy(items, m.queue)
	return func() tea.Msg {
		client := ctx.S3InRegion(region)
		out := make(map[string]bool, len(items))
		sem := make(chan struct{}, headProbeConcurrency)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, it := range items {
			wg.Add(1)
			sem <- struct{}{}
			go func(k string) {
				defer wg.Done()
				defer func() { <-sem }()
				_, err := client.HeadObject(context.Background(), &s3sdk.HeadObjectInput{
					Bucket: awssdk.String(bucket),
					Key:    awssdk.String(k),
				})
				exists := false
				if err == nil {
					exists = true
				} else {
					var apiErr smithy.APIError
					if errors.As(err, &apiErr) {
						// "NotFound" / "NoSuchKey" -> not exists; anything else -> assume not present.
						_ = apiErr
					}
				}
				mu.Lock()
				out[k] = exists
				mu.Unlock()
			}(it.key)
		}
		wg.Wait()
		return headProbeDoneMsg{exists: out}
	}
}

func (m uploadModel) uploadStepCmd(idx int) tea.Cmd {
	ctx := m.ctx
	region := m.region
	bucket := m.bucket
	it := m.queue[idx]
	return func() tea.Msg {
		f, err := os.Open(it.path)
		if err != nil {
			return uploadStepMsg{index: idx, path: it.path, key: it.key, err: err}
		}
		defer f.Close()
		tm := transfermanager.New(ctx.S3InRegion(region))
		_, err = tm.UploadObject(context.Background(), &transfermanager.UploadObjectInput{
			Bucket: awssdk.String(bucket),
			Key:    awssdk.String(it.key),
			Body:   f,
		})
		return uploadStepMsg{index: idx, path: it.path, key: it.key, err: err}
	}
}

func (m uploadModel) View() string {
	title := headerStyle.Render(fmt.Sprintf("Upload to s3://%s/%s", m.bucket, m.prefix))

	switch m.stage {
	case stagePicking:
		return m.viewPicking(title)
	case stageProbing:
		return lipgloss.JoinVertical(lipgloss.Left, title, "", m.loader.Render("checking for existing keys..."))
	case stageConfirming:
		return m.viewConfirming(title)
	case stageUploading:
		return m.viewUploading(title)
	case stageDone:
		return m.viewDone(title)
	}
	return title
}

func (m uploadModel) viewPicking(title string) string {
	queueLines := []string{mutedStyle.Render("Queued files (" + fmt.Sprint(len(m.queue)) + "):")}
	if len(m.queue) == 0 {
		queueLines = append(queueLines, mutedStyle.Render("  (none) - enter selects/toggles a file"))
	} else {
		for _, it := range m.queue {
			queueLines = append(queueLines, fmt.Sprintf("  %s  %s -> %s", humanBytes(it.size), filepath.Base(it.path), it.key))
		}
	}
	hint := mutedStyle.Render("enter: toggle file in queue · U: upload · h/backspace: up a dir · esc: cancel")
	parts := []string{
		title,
		mutedStyle.Render(m.picker.CurrentDirectory),
		m.picker.View(),
		"",
		strings.Join(queueLines, "\n"),
	}
	if m.status != "" {
		parts = append(parts, "", mutedStyle.Render(m.status))
	}
	parts = append(parts, "", hint)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m uploadModel) viewConfirming(title string) string {
	var collisions []string
	for _, it := range m.queue {
		if m.exists[it.key] {
			collisions = append(collisions, "  "+it.key)
		}
	}
	body := lipgloss.JoinVertical(lipgloss.Left,
		errorStyle.Render(fmt.Sprintf("%d key(s) already exist in the bucket:", len(collisions))),
		strings.Join(collisions, "\n"),
		"",
		"Overwrite all? (y) yes  (n) back to picker",
	)
	return lipgloss.JoinVertical(lipgloss.Left, title, "", body)
}

func (m uploadModel) viewUploading(title string) string {
	done := len(m.results)
	total := len(m.queue)
	var doneLines []string
	for _, r := range m.results {
		if r.err != nil {
			doneLines = append(doneLines, errorStyle.Render("  ✗ "+filepath.Base(r.path)+": "+r.err.Error()))
		} else {
			doneLines = append(doneLines, mutedStyle.Render("  ✓ "+filepath.Base(r.path)))
		}
	}
	current := ""
	if done < total {
		current = m.loader.Render(fmt.Sprintf("uploading %d/%d: %s", done+1, total, filepath.Base(m.queue[done].path)))
	}
	parts := []string{title, ""}
	if len(doneLines) > 0 {
		parts = append(parts, strings.Join(doneLines, "\n"))
	}
	if current != "" {
		parts = append(parts, current)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m uploadModel) viewDone(title string) string {
	ok, fail := 0, 0
	var lines []string
	for _, r := range m.results {
		if r.err != nil {
			fail++
			lines = append(lines, errorStyle.Render("  ✗ "+filepath.Base(r.path)+": "+r.err.Error()))
		} else {
			ok++
			lines = append(lines, mutedStyle.Render("  ✓ "+filepath.Base(r.path)+" -> "+r.key))
		}
	}
	summary := fmt.Sprintf("Done. %d uploaded, %d failed.", ok, fail)
	parts := []string{title, "", summary, ""}
	parts = append(parts, lines...)
	parts = append(parts, "", mutedStyle.Render("enter/esc to close"))
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m uploadModel) CapturingInput() bool {
	return m.stage == stagePicking || m.stage == stageConfirming
}

func (m uploadModel) HelpItems() []help.Section {
	switch m.stage {
	case stagePicking:
		return []help.Section{{
			Title: "S3 · upload (pick files)",
			Items: []help.Item{
				{Keys: "enter", Desc: "toggle file in queue / enter directory"},
				{Keys: "h / backspace", Desc: "up a directory"},
				{Keys: "j/k ↓/↑", Desc: "move cursor"},
				{Keys: "U", Desc: "upload queued files"},
				{Keys: "esc", Desc: "cancel and return to browser"},
			},
		}}
	case stageConfirming:
		return []help.Section{{
			Title: "S3 · upload (confirm overwrite)",
			Items: []help.Item{
				{Keys: "y", Desc: "overwrite existing keys"},
				{Keys: "n / esc", Desc: "back to picker"},
			},
		}}
	}
	return []help.Section{{
		Title: "S3 · upload",
		Items: []help.Item{
			{Keys: "esc", Desc: "close when finished"},
		},
	}}
}
