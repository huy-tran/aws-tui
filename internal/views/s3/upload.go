package s3

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
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
	dirLoadedMsg struct {
		dir     string
		entries []localEntry
		err     error
	}
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

type localEntry struct {
	name     string
	path     string
	isDir    bool
	size     int64
	modified time.Time
}

type uploadItem struct {
	path string
	key  string
	size int64
}

type uploadModel struct {
	ctx    *awspkg.Context
	region string
	bucket string
	prefix string

	dir       string
	dirHist   []string
	entries   []localEntry
	displayed []localEntry
	queued    map[string]bool // local path -> queued

	table      datatable.Model
	filter     textinput.Model
	filterMode bool

	stage   uploadStage
	queue   []uploadItem
	exists  map[string]bool
	results []uploadStepMsg

	loader loader.Model
	err    error
	status string

	width, height int
}

func newUpload(ctx *awspkg.Context, region, bucket, prefix string) uploadModel {
	dir := "."
	if home, err := os.UserHomeDir(); err == nil {
		dir = home
	}

	cols := []datatable.Column{
		{Title: "Name", Flex: true},
		{Title: "Size", Align: lipgloss.Right, SortAs: datatable.SortNumeric},
		{Title: "Modified"},
	}
	t := datatable.New(cols)
	t.SetHeight(15)

	ti := textinput.New()
	ti.Placeholder = "filter"
	ti.CharLimit = 64
	ti.Prompt = "/ "

	return uploadModel{
		ctx:    ctx,
		region: region,
		bucket: bucket,
		prefix: prefix,
		dir:    dir,
		queued: make(map[string]bool),
		table:  t,
		filter: ti,
		loader: loader.New(),
	}
}

func (m uploadModel) Init() tea.Cmd {
	return loadDirCmd(m.dir)
}

func loadDirCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		raw, err := os.ReadDir(dir)
		if err != nil {
			return dirLoadedMsg{dir: dir, err: err}
		}
		entries := make([]localEntry, 0, len(raw))
		for _, de := range raw {
			info, err := de.Info()
			if err != nil {
				continue
			}
			entries = append(entries, localEntry{
				name:     de.Name(),
				path:     filepath.Join(dir, de.Name()),
				isDir:    de.IsDir(),
				size:     info.Size(),
				modified: info.ModTime(),
			})
		}
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].isDir != entries[j].isDir {
				return entries[i].isDir
			}
			return strings.ToLower(entries[i].name) < strings.ToLower(entries[j].name)
		})
		return dirLoadedMsg{dir: dir, entries: entries}
	}
}

func (m uploadModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 12
		if h < 5 {
			h = 5
		}
		m.table.SetHeight(h)
		m.table.SetWidth(msg.Width)
		return m, nil

	case dirLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.dir = msg.dir
		m.entries = msg.entries
		m.err = nil
		m.applyFilter()
		m.table.GotoTop()
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
		if m.filterMode {
			switch msg.String() {
			case "esc":
				m.filterMode = false
				m.filter.SetValue("")
				m.filter.Blur()
				m.applyFilter()
				return m, nil
			case "enter":
				m.filterMode = false
				m.filter.Blur()
				return m, nil
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.applyFilter()
			return m, cmd
		}

		switch msg.String() {
		case "esc":
			return m, nav.PopView()
		case "/":
			m.filterMode = true
			m.filter.Focus()
			return m, textinput.Blink
		case "U":
			if len(m.queue) == 0 {
				m.status = "queue is empty - press enter on a file to add it"
				return m, nil
			}
			m.stage = stageProbing
			m.status = ""
			return m, tea.Batch(m.loader.Tick(), m.headProbeCmd())
		case "backspace", "h", "left":
			return m.goUp()
		case "enter", "l", "right":
			return m.activateCursor()
		}

		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
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
		return m, nil

	case stageDone:
		if msg.String() == "esc" || msg.String() == "enter" {
			return m, tea.Batch(nav.PopView(), func() tea.Msg { return refreshPrefixMsg{} })
		}
		return m, nil
	}
	return m, nil
}

func (m uploadModel) goUp() (tea.Model, tea.Cmd) {
	parent := filepath.Dir(m.dir)
	if parent == m.dir {
		return m, nil
	}
	m.dirHist = append(m.dirHist, m.dir)
	m.filter.SetValue("")
	return m, loadDirCmd(parent)
}

func (m uploadModel) activateCursor() (tea.Model, tea.Cmd) {
	e := m.cursorEntry()
	if e == nil {
		return m, nil
	}
	if e.isDir {
		m.dirHist = append(m.dirHist, m.dir)
		m.filter.SetValue("")
		return m, loadDirCmd(e.path)
	}
	// File: toggle in queue.
	if m.queued[e.path] {
		delete(m.queued, e.path)
		for i, it := range m.queue {
			if it.path == e.path {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
	} else {
		m.queued[e.path] = true
		m.queue = append(m.queue, uploadItem{
			path: e.path,
			key:  m.prefix + e.name,
			size: e.size,
		})
	}
	m.applyFilter()
	m.status = ""
	return m, nil
}

func (m uploadModel) cursorEntry() *localEntry {
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.displayed) {
		return nil
	}
	return &m.displayed[idx]
}

func (m *uploadModel) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		m.displayed = m.entries
	} else {
		out := make([]localEntry, 0, len(m.entries))
		for _, e := range m.entries {
			if strings.Contains(strings.ToLower(e.name), q) {
				out = append(out, e)
			}
		}
		m.displayed = out
	}
	m.table.SetRows(buildLocalRows(m.displayed, m.queued))
}

func buildLocalRows(entries []localEntry, queued map[string]bool) []datatable.Row {
	rows := make([]datatable.Row, len(entries))
	for i, e := range entries {
		name := e.name
		if e.isDir {
			name = folderStyle.Render(name + "/")
		} else if queued[e.path] {
			name = queuedFileStyle.Render("✓ " + name)
		}
		size := "-"
		if !e.isDir {
			size = humanBytes(e.size)
		}
		modified := e.modified.Local().Format("2006-01-02 15:04")
		rows[i] = datatable.Row{name, size, modified}
	}
	return rows
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
	dirLine := mutedStyle.Render(m.dir)
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}

	body := m.table.View()
	if m.err != nil {
		body = errorStyle.Render("Error: " + m.err.Error())
	} else if len(m.displayed) == 0 {
		if m.filter.Value() != "" {
			body = mutedStyle.Render("No entries match filter.")
		} else {
			body = mutedStyle.Render("(empty directory)")
		}
	}

	queueLines := []string{mutedStyle.Render(fmt.Sprintf("Queued files (%d):", len(m.queue)))}
	if len(m.queue) == 0 {
		queueLines = append(queueLines, mutedStyle.Render("  (none) - enter on a file adds it; folders descend"))
	} else {
		for _, it := range m.queue {
			queueLines = append(queueLines, fmt.Sprintf("  %s  %s -> %s", humanBytes(it.size), filepath.Base(it.path), it.key))
		}
	}

	hint := mutedStyle.Render("enter: enter dir / toggle file · /: filter · U: upload · backspace: up a dir · esc: cancel")

	parts := []string{title, dirLine, filterLine, body, "", strings.Join(queueLines, "\n")}
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
	return m.filterMode || m.stage == stageConfirming
}

func (m uploadModel) HelpItems() []help.Section {
	switch m.stage {
	case stagePicking:
		if m.filterMode {
			return []help.Section{{
				Title: "S3 · upload (filter)",
				Items: []help.Item{
					{Keys: "type", Desc: "filter entries by name"},
					{Keys: "enter", Desc: "apply and exit filter"},
					{Keys: "esc", Desc: "clear and exit filter"},
				},
			}}
		}
		return []help.Section{{
			Title: "S3 · upload (pick files)",
			Items: []help.Item{
				{Keys: "enter / l", Desc: "enter directory / toggle file in queue"},
				{Keys: "backspace / h", Desc: "up a directory"},
				{Keys: "/", Desc: "filter entries"},
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

var (
	folderStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true)
	queuedFileStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Bold(true)
)
