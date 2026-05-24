package ec2

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
)

// portForwardModel collects ports + optional remote host then shells out to
// `aws ssm start-session` with the appropriate SSM document.
type portForwardModel struct {
	ctx      *awspkg.Context
	inst     Instance
	inputs   [3]textinput.Model
	focusIdx int
	errMsg   string
	running  bool
}

const (
	idxRemotePort = 0
	idxLocalPort  = 1
	idxRemoteHost = 2
)

type pfDoneMsg struct{ err error }

func newPortForward(ctx *awspkg.Context, inst Instance) portForwardModel {
	mk := func(placeholder string, charLimit int) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.CharLimit = charLimit
		ti.Width = 30
		return ti
	}

	m := portForwardModel{ctx: ctx, inst: inst}
	m.inputs[idxRemotePort] = mk("e.g. 3306", 5)
	m.inputs[idxLocalPort] = mk("(defaults to remote)", 5)
	m.inputs[idxRemoteHost] = mk("(optional, e.g. db.internal)", 200)
	m.inputs[idxRemotePort].Focus()
	return m
}

func (m portForwardModel) Init() tea.Cmd { return textinput.Blink }

func (m portForwardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.running {
		if done, ok := msg.(pfDoneMsg); ok {
			if done.err != nil {
				m.errMsg = done.err.Error()
				m.running = false
				return m, nil
			}
			return m, nav.PopView()
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return m, nav.PopView()
		case "tab", "down":
			return m.focus((m.focusIdx + 1) % len(m.inputs)), nil
		case "shift+tab", "up":
			return m.focus((m.focusIdx - 1 + len(m.inputs)) % len(m.inputs)), nil
		case "enter":
			cmd, err := m.startCmd()
			if err != nil {
				m.errMsg = err.Error()
				return m, nil
			}
			m.errMsg = ""
			m.running = true
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.inputs[m.focusIdx], cmd = m.inputs[m.focusIdx].Update(msg)
	return m, cmd
}

func (m portForwardModel) focus(idx int) portForwardModel {
	m.inputs[m.focusIdx].Blur()
	m.focusIdx = idx
	m.inputs[m.focusIdx].Focus()
	return m
}

func (m portForwardModel) startCmd() (tea.Cmd, error) {
	remotePort := strings.TrimSpace(m.inputs[idxRemotePort].Value())
	localPort := strings.TrimSpace(m.inputs[idxLocalPort].Value())
	remoteHost := strings.TrimSpace(m.inputs[idxRemoteHost].Value())

	if err := validatePort(remotePort); err != nil {
		return nil, fmt.Errorf("remote port: %w", err)
	}
	if localPort == "" {
		localPort = remotePort
	} else if err := validatePort(localPort); err != nil {
		return nil, fmt.Errorf("local port: %w", err)
	}

	args := []string{"ssm", "start-session",
		"--profile", m.ctx.Profile,
		"--region", m.ctx.Region,
		"--target", m.inst.ID,
	}
	if remoteHost == "" {
		args = append(args,
			"--document-name", "AWS-StartPortForwardingSession",
			"--parameters", fmt.Sprintf(`{"portNumber":["%s"],"localPortNumber":["%s"]}`, remotePort, localPort),
		)
	} else {
		args = append(args,
			"--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
			"--parameters", fmt.Sprintf(`{"host":["%s"],"portNumber":["%s"],"localPortNumber":["%s"]}`,
				remoteHost, remotePort, localPort),
		)
	}

	cmd := execAWS(args...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return pfDoneMsg{err: err}
	}), nil
}

func validatePort(s string) error {
	if s == "" {
		return fmt.Errorf("required")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("not a number")
	}
	if n < 1 || n > 65535 {
		return fmt.Errorf("must be 1-65535")
	}
	return nil
}

func (m portForwardModel) View() string {
	title := headerStyle.Render(fmt.Sprintf("Port forward via %s (%s)", m.inst.ID, defaultStr(m.inst.Name, "no name")))
	context := mutedStyle.Render(fmt.Sprintf("%s · %s · %s", m.ctx.Profile, m.ctx.Region, m.inst.PrivIP))

	labels := []string{"Remote port", "Local port", "Remote host"}
	var rows []string
	for i, label := range labels {
		marker := "  "
		if i == m.focusIdx {
			marker = "▸ "
		}
		rows = append(rows, fmt.Sprintf("%s%-12s %s", marker, label+":", m.inputs[i].View()))
	}

	help := mutedStyle.Render("tab to move · enter to start · esc to cancel")
	body := strings.Join(rows, "\n")

	parts := []string{title, context, "", body, ""}
	if m.errMsg != "" {
		parts = append(parts, errorStyle.Render(m.errMsg), "")
	}
	if m.running {
		parts = append(parts, mutedStyle.Render("session active — return here when the session ends"), "")
	}
	parts = append(parts, help)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}
