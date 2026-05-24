package s3

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
)

type (
	downloadDoneMsg struct{ path string }
	downloadFailMsg struct{ err error }
)

type downloadModel struct {
	ctx    *awspkg.Context
	region string
	bucket string
	obj    entry

	dest    textinput.Model
	running bool
	done    bool
	err     error
	status  string
}

func newDownload(ctx *awspkg.Context, region, bucket string, obj entry) downloadModel {
	home, _ := os.UserHomeDir()
	defaultPath := filepath.Join(home, "Downloads", filepath.Base(strings.TrimSuffix(obj.name, "/")))

	ti := textinput.New()
	ti.SetValue(defaultPath)
	ti.CharLimit = 4096
	ti.Width = 60
	ti.Focus()

	return downloadModel{ctx: ctx, region: region, bucket: bucket, obj: obj, dest: ti}
}

func (m downloadModel) Init() tea.Cmd { return textinput.Blink }

func (m downloadModel) downloadCmd() tea.Cmd {
	ctx := m.ctx
	region := m.region
	bucket := m.bucket
	key := m.obj.name
	dest := m.dest.Value()
	return func() tea.Msg {
		client := ctx.S3InRegion(region)
		out, err := client.GetObject(context.Background(), &s3sdk.GetObjectInput{
			Bucket: awssdk.String(bucket),
			Key:    awssdk.String(key),
		})
		if err != nil {
			return downloadFailMsg{err: err}
		}
		defer out.Body.Close()

		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return downloadFailMsg{err: err}
		}
		f, err := os.Create(dest)
		if err != nil {
			return downloadFailMsg{err: err}
		}
		defer f.Close()
		if _, err := io.Copy(f, out.Body); err != nil {
			return downloadFailMsg{err: err}
		}
		return downloadDoneMsg{path: dest}
	}
}

func (m downloadModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case downloadDoneMsg:
		m.running = false
		m.done = true
		m.status = "saved to " + msg.path
		return m, nil
	case downloadFailMsg:
		m.running = false
		m.err = msg.err
		return m, nil
	case tea.KeyMsg:
		if m.running {
			return m, nil
		}
		switch msg.String() {
		case "esc":
			return m, nav.PopView()
		case "enter":
			if m.done {
				return m, nav.PopView()
			}
			if strings.TrimSpace(m.dest.Value()) == "" {
				m.status = "destination required"
				return m, nil
			}
			m.running = true
			m.err = nil
			m.status = ""
			return m, m.downloadCmd()
		}
		var cmd tea.Cmd
		m.dest, cmd = m.dest.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m downloadModel) View() string {
	title := headerStyle.Render("Download object")
	info := fmt.Sprintf("Key:  %s\nSize: %s", m.obj.name, humanBytes(m.obj.size))
	prompt := "Save to:"
	help := mutedStyle.Render("enter to download · esc to cancel")
	if m.done {
		help = mutedStyle.Render("enter to close")
	}

	parts := []string{title, "", info, "", prompt, m.dest.View(), ""}
	if m.err != nil {
		parts = append(parts, errorStyle.Render("Error: "+m.err.Error()), "")
	}
	if m.running {
		parts = append(parts, mutedStyle.Render("downloading..."), "")
	}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status), "")
	}
	parts = append(parts, help)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}
