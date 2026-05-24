# 07 — EC2 View

The most important view. Lists EC2 instances and lets the user open an SSM session or port forward with one keypress.

## Layout

```
┌─ EC2 Instances (12 running, 3 stopped) ──────────────────────────┐
│ Filter: ____________                                              │
│                                                                   │
│   Name                  Instance ID         State    Type   IP    │
│ > app-prod-web-01       i-0abc123def456789  running  t3.med 10.0.. │
│   app-prod-web-02       i-0def456abc789012  running  t3.med 10.0.. │
│   app-prod-worker-01    i-0aaa111bbb222ccc  running  t3.lar 10.0.. │
│   bastion               i-0xyz999aaa111bbb  running  t3.mic 10.0.. │
│   db-replica-old        i-0fff222eee333ddd  stopped  m5.lar -      │
│                                                                   │
│ enter: SSM session · p: port forward · i: details · r: refresh   │
└───────────────────────────────────────────────────────────────────┘
```

## Implementation outline (internal/views/ec2/ec2.go)

```go
package ec2

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/YOUR_USERNAME/aws-tui/internal/aws"
)

type Instance struct {
	ID       string
	Name     string
	State    string
	Type     string
	PrivIP   string
	PubIP    string
	VPCID    string
	AZ       string
	Platform string
}

type loadedMsg struct{ instances []Instance }
type errMsg struct{ err error }

type Model struct {
	ctx       *awspkg.Context
	table     table.Model
	spinner   spinner.Model
	instances []Instance
	loading   bool
	err       error
	width     int
	height    int
}

func New(ctx *awspkg.Context) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	columns := []table.Column{
		{Title: "Name", Width: 28},
		{Title: "Instance ID", Width: 20},
		{Title: "State", Width: 10},
		{Title: "Type", Width: 12},
		{Title: "Priv IP", Width: 15},
	}
	t := table.New(
		table.WithColumns(columns),
		table.WithFocused(true),
		table.WithHeight(20),
	)
	return Model{ctx: ctx, table: t, spinner: sp, loading: true}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadCmd())
}

func (m Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		if err := m.ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := m.ctx.EC2()
		var instances []Instance
		paginator := ec2sdk.NewDescribeInstancesPaginator(client, &ec2sdk.DescribeInstancesInput{})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(context.Background())
			if err != nil {
				return errMsg{err: err}
			}
			for _, res := range page.Reservations {
				for _, inst := range res.Instances {
					instances = append(instances, parseInstance(inst))
				}
			}
		}
		return loadedMsg{instances: instances}
	}
}

func parseInstance(inst types.Instance) Instance {
	out := Instance{
		ID:    aws.ToString(inst.InstanceId),
		State: string(inst.State.Name),
		Type:  string(inst.InstanceType),
		PrivIP: aws.ToString(inst.PrivateIpAddress),
		PubIP:  aws.ToString(inst.PublicIpAddress),
		VPCID:  aws.ToString(inst.VpcId),
		AZ:     aws.ToString(inst.Placement.AvailabilityZone),
	}
	if inst.Platform == types.PlatformValuesWindows {
		out.Platform = "windows"
	} else {
		out.Platform = "linux"
	}
	for _, tag := range inst.Tags {
		if aws.ToString(tag.Key) == "Name" {
			out.Name = aws.ToString(tag.Value)
		}
	}
	return out
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.table.SetHeight(msg.Height - 4)
		return m, nil

	case loadedMsg:
		m.loading = false
		m.instances = msg.instances
		m.table.SetRows(buildRows(m.instances))
		return m, nil

	case errMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "r":
			m.loading = true
			m.err = nil
			return m, tea.Batch(m.spinner.Tick, m.loadCmd())
		case "enter":
			if inst := m.selected(); inst != nil && inst.State == "running" {
				return m, m.startSSMSession(*inst)
			}
		case "p":
			if inst := m.selected(); inst != nil && inst.State == "running" {
				return m, m.startPortForward(*inst)
			}
		case "i":
			if inst := m.selected(); inst != nil {
				return m, m.showDetails(*inst)
			}
		}
	}

	var cmd tea.Cmd
	if m.loading {
		m.spinner, cmd = m.spinner.Update(msg)
	} else {
		m.table, cmd = m.table.Update(msg)
	}
	return m, cmd
}

func (m Model) selected() *Instance {
	if m.loading || len(m.instances) == 0 {
		return nil
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.instances) {
		return nil
	}
	return &m.instances[idx]
}

func (m Model) View() string {
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\nPress r to retry.", m.err))
	}
	if m.loading {
		return fmt.Sprintf("%s loading instances...", m.spinner.View())
	}
	if len(m.instances) == 0 {
		return "No instances in this region."
	}
	return m.table.View()
}

func buildRows(instances []Instance) []table.Row {
	rows := make([]table.Row, len(instances))
	for i, inst := range instances {
		rows[i] = table.Row{
			truncate(inst.Name, 28),
			inst.ID,
			inst.State,
			inst.Type,
			inst.PrivIP,
		}
	}
	return rows
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

var errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
```

## SSM session (key bit)

The TUI must suspend itself, run `aws ssm start-session`, then resume:

```go
import (
	"os/exec"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) startSSMSession(inst Instance) tea.Cmd {
	cmd := exec.Command("aws", "ssm", "start-session",
		"--profile", m.ctx.Profile,
		"--region", m.ctx.Region,
		"--target", inst.ID,
	)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return errMsg{err: fmt.Errorf("ssm session failed: %w", err)}
		}
		return nil
	})
}
```

`tea.ExecProcess` handles the suspend/resume automatically. The user sees the SSM session take over the full terminal, then returns to the TUI on exit.

## Port forward

Common use case: port forward to RDS through a bastion, or to an internal service.

```go
func (m Model) startPortForward(inst Instance) tea.Cmd {
	// Show a small modal asking for: remote port, local port, optional remote host
	// (for AWS-StartPortForwardingSessionToRemoteHost)
	// For v1, simpler: prompt for remote_port and local_port only
	return m.pushPortForwardModal(inst)
}
```

The modal collects:
- **Local port** (default: same as remote)
- **Remote port** (e.g. 3306 for RDS)
- **Remote host** (optional; if set, uses `AWS-StartPortForwardingSessionToRemoteHost`)

Then runs:

```go
args := []string{"ssm", "start-session",
	"--profile", m.ctx.Profile,
	"--region", m.ctx.Region,
	"--target", inst.ID,
}
if remoteHost == "" {
	args = append(args, "--document-name", "AWS-StartPortForwardingSession",
		"--parameters", fmt.Sprintf(`{"portNumber":["%s"],"localPortNumber":["%s"]}`, remotePort, localPort))
} else {
	args = append(args, "--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
		"--parameters", fmt.Sprintf(`{"host":["%s"],"portNumber":["%s"],"localPortNumber":["%s"]}`,
			remoteHost, remotePort, localPort))
}
cmd := exec.Command("aws", args...)
return tea.ExecProcess(cmd, ...)
```

## Filtering

Use the table's built-in row filtering, OR add a textinput above the table that filters `m.instances` and rebuilds rows on every keystroke. Filter by name, ID, IP, and tag values.

## Details panel (`i` key)

Push a modal view showing the full instance:

- All tags
- Security groups
- IAM role
- AMI ID
- Launch time
- Subnet, VPC, AZ
- Public DNS

Copy-to-clipboard hotkeys: `y i` (yank instance ID), `y p` (yank private IP), `y d` (yank public DNS). Use `github.com/atotto/clipboard`.

## Acceptance criteria

- `Init()` triggers a single paginated `DescribeInstances` call.
- Loading shows a spinner. Errors show with a retry hint.
- Table shows Name, ID, State, Type, Private IP.
- `enter` on a running instance opens an SSM session via `aws ssm start-session`, suspending the TUI.
- `p` on a running instance opens a port forward prompt then starts the session.
- `i` shows details panel with yank hotkeys.
- `r` refreshes the list.
