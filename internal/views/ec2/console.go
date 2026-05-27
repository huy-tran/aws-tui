package ec2

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/ui/loader"
)

// consoleModel is pushed onto the view stack when the user presses 'c' on
// an EC2 instance. It runs GetConsoleOutput and renders the (base64-decoded)
// output in a scrollable viewport - useful when the instance is wedged in
// a boot loop, the SSM agent is dead, or SSH won't come up.
type consoleModel struct {
	ctx     *awspkg.Context
	inst    Instance
	output  string
	loader  loader.Model
	vp      viewport.Model
	loading bool
	err     error
	status  string
	width   int
	height  int
}

type consoleLoadedMsg struct {
	output string
	err    error
}

func newConsole(ctx *awspkg.Context, inst Instance) consoleModel {
	vp := viewport.New(0, 0)
	return consoleModel{
		ctx:     ctx,
		inst:    inst,
		vp:      vp,
		loader:  loader.New(),
		loading: true,
	}
}

func (m consoleModel) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.fetchCmd(false))
}

// fetchCmd calls GetConsoleOutput. latest=true requests the most recent
// 64KB; without it AWS may return cached output up to an hour old.
func (m consoleModel) fetchCmd(latest bool) tea.Cmd {
	ctx := m.ctx
	id := m.inst.ID
	return func() tea.Msg {
		if err := ctx.Load(context.Background()); err != nil {
			return consoleLoadedMsg{err: err}
		}
		client := ctx.EC2()
		in := &ec2sdk.GetConsoleOutputInput{InstanceId: awssdk.String(id)}
		if latest {
			in.Latest = awssdk.Bool(true)
		}
		out, err := client.GetConsoleOutput(context.Background(), in)
		if err != nil {
			return consoleLoadedMsg{err: err}
		}
		raw := awssdk.ToString(out.Output)
		// AWS returns the output base64-encoded; failures here mean we
		// just show whatever the API gave us.
		if decoded, decErr := base64.StdEncoding.DecodeString(raw); decErr == nil {
			raw = string(decoded)
		}
		return consoleLoadedMsg{output: raw}
	}
}

func (m consoleModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 5
		if h < 5 {
			h = 5
		}
		m.vp.Width = msg.Width
		m.vp.Height = h
		if m.output != "" {
			m.vp.SetContent(m.output)
		}
		return m, nil

	case consoleLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.output = msg.output
		if strings.TrimSpace(m.output) == "" {
			m.output = "(no console output available - instance may not have produced any yet, or it has been recycled)"
		}
		m.vp.SetContent(m.output)
		m.vp.GotoBottom()
		return m, nil

	case spinner.TickMsg:
		if !m.loading {
			return m, nil
		}
		var cmd tea.Cmd
		m.loader, cmd = m.loader.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return m, nav.PopView()
		case "r":
			m.loading = true
			m.err = nil
			m.output = ""
			m.vp.SetContent("")
			return m, tea.Batch(m.loader.Tick(), m.fetchCmd(true))
		case "y":
			if m.output == "" {
				m.status = "no output to copy"
				return m, nil
			}
			if err := clipboard.WriteAll(m.output); err != nil {
				m.status = "clipboard error: " + err.Error()
			} else {
				m.status = "copied console output"
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m consoleModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Render("Console output: " + m.inst.ID)
	if m.inst.Name != "" {
		title += "  (" + m.inst.Name + ")"
	}
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\nPress r to retry, esc to back.", m.err))
	}
	if m.loading {
		return lipgloss.JoinVertical(lipgloss.Left, title, "", m.loader.Render("fetching console output..."))
	}
	help := mutedStyle.Render("↑/↓ scroll · r: refresh (latest) · y: yank · esc: back")
	parts := []string{title, "", m.vp.View(), help}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}
