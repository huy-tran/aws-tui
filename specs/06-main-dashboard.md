# 06 — Main Dashboard

The tabbed container shown after profile + region are selected. Each tab is a service view. The dashboard owns the context header, tab bar, and routes keys to the active tab.

## Layout

```
┌──────────────────────────────────────────────────────────────┐
│ prod-client-a │ ap-southeast-2 │ EC2                         │  ← header
├──────────────────────────────────────────────────────────────┤
│ [E]C2  [C]loudFront  [S]3  [B]eanstalk  [L]ogs               │  ← tabs
├──────────────────────────────────────────────────────────────┤
│                                                              │
│ (active tab content)                                         │
│                                                              │
│                                                              │
├──────────────────────────────────────────────────────────────┤
│ ↑/↓ nav · enter open · r refresh · ctrl+p profile · ? help   │  ← footer
└──────────────────────────────────────────────────────────────┘
```

## Header colour coding

Match the profile name against patterns:

| Pattern        | Colour                |
|----------------|-----------------------|
| `prod*`        | Red background        |
| `staging*`     | Yellow background     |
| `dev*`, `test*`| Green background      |
| anything else  | Blue background       |

This is the single most important safety feature. A user looking at red knows they're in prod and will think twice before destructive actions.

## Implementation (internal/views/dashboard/dashboard.go)

```go
package dashboard

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/YOUR_USERNAME/aws-tui/internal/aws"
	"github.com/YOUR_USERNAME/aws-tui/internal/views/cloudfront"
	"github.com/YOUR_USERNAME/aws-tui/internal/views/cloudwatch"
	"github.com/YOUR_USERNAME/aws-tui/internal/views/ec2"
	"github.com/YOUR_USERNAME/aws-tui/internal/views/beanstalk"
	"github.com/YOUR_USERNAME/aws-tui/internal/views/s3"
)

type Tab int

const (
	TabEC2 Tab = iota
	TabCloudFront
	TabS3
	TabBeanstalk
	TabLogs
)

var tabNames = []string{"EC2", "CloudFront", "S3", "Beanstalk", "Logs"}
var tabHotkeys = []string{"e", "c", "s", "b", "l"}

type Model struct {
	ctx     *awspkg.Context
	active  Tab
	tabs    [5]tea.Model
	width   int
	height  int
}

func New(ctx *awspkg.Context) Model {
	m := Model{ctx: ctx, active: TabEC2}
	m.tabs[TabEC2] = ec2.New(ctx)
	m.tabs[TabCloudFront] = cloudfront.New(ctx)
	m.tabs[TabS3] = s3.New(ctx)
	m.tabs[TabBeanstalk] = beanstalk.New(ctx)
	m.tabs[TabLogs] = cloudwatch.New(ctx)
	return m
}

func (m Model) Init() tea.Cmd {
	// Only initialise the first tab; others init on first activation
	return m.tabs[m.active].Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Tabs need to know inner dimensions (minus header/footer/tabbar)
		inner := tea.WindowSizeMsg{
			Width:  msg.Width,
			Height: msg.Height - 4, // header(1) + tabs(1) + footer(1) + borders
		}
		for i := range m.tabs {
			updated, _ := m.tabs[i].Update(inner)
			m.tabs[i] = updated
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "e":
			return m.switchTab(TabEC2)
		case "c":
			return m.switchTab(TabCloudFront)
		case "s":
			return m.switchTab(TabS3)
		case "b":
			return m.switchTab(TabBeanstalk)
		case "l":
			return m.switchTab(TabLogs)
		case "tab":
			return m.switchTab((m.active + 1) % Tab(len(m.tabs)))
		case "shift+tab":
			return m.switchTab((m.active - 1 + Tab(len(m.tabs))) % Tab(len(m.tabs)))
		}
	}

	// Delegate everything else to the active tab
	updated, cmd := m.tabs[m.active].Update(msg)
	m.tabs[m.active] = updated
	return m, cmd
}

func (m Model) switchTab(t Tab) (tea.Model, tea.Cmd) {
	if t == m.active {
		return m, nil
	}
	m.active = t
	return m, m.tabs[t].Init()
}

func (m Model) View() string {
	header := m.renderHeader()
	tabs := m.renderTabs()
	footer := m.renderFooter()

	contentHeight := m.height - lipgloss.Height(header) - lipgloss.Height(tabs) - lipgloss.Height(footer)
	content := lipgloss.NewStyle().Height(contentHeight).Render(m.tabs[m.active].View())

	return lipgloss.JoinVertical(lipgloss.Left, header, tabs, content, footer)
}

func (m Model) renderHeader() string {
	profileStyle := profileColour(m.ctx.Profile).
		Padding(0, 1).
		Bold(true)
	regionStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("237")).
		Foreground(lipgloss.Color("252")).
		Padding(0, 1)
	serviceStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("240")).
		Foreground(lipgloss.Color("252")).
		Padding(0, 1)

	return lipgloss.JoinHorizontal(lipgloss.Top,
		profileStyle.Render(m.ctx.Profile),
		regionStyle.Render(m.ctx.Region),
		serviceStyle.Render(tabNames[m.active]),
	)
}

func (m Model) renderTabs() string {
	var parts []string
	for i, name := range tabNames {
		label := "[" + strings.ToUpper(tabHotkeys[i]) + "]" + name[1:]
		style := lipgloss.NewStyle().Padding(0, 2)
		if Tab(i) == m.active {
			style = style.Bold(true).Underline(true).Foreground(lipgloss.Color("213"))
		} else {
			style = style.Foreground(lipgloss.Color("245"))
		}
		parts = append(parts, style.Render(label))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func (m Model) renderFooter() string {
	help := "↑/↓ nav · enter open · r refresh · ctrl+p profile · ctrl+r region · ? help · ctrl+c quit"
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(0, 1).
		Render(help)
}

func profileColour(profile string) lipgloss.Style {
	p := strings.ToLower(profile)
	switch {
	case strings.HasPrefix(p, "prod"):
		return lipgloss.NewStyle().
			Background(lipgloss.Color("160")). // red
			Foreground(lipgloss.Color("231"))
	case strings.HasPrefix(p, "staging"):
		return lipgloss.NewStyle().
			Background(lipgloss.Color("214")). // orange/yellow
			Foreground(lipgloss.Color("232"))
	case strings.HasPrefix(p, "dev"), strings.HasPrefix(p, "test"):
		return lipgloss.NewStyle().
			Background(lipgloss.Color("34")). // green
			Foreground(lipgloss.Color("231"))
	default:
		return lipgloss.NewStyle().
			Background(lipgloss.Color("33")). // blue
			Foreground(lipgloss.Color("231"))
	}
}
```

## Lazy tab loading

`Init()` is only called for the active tab. When switching tabs, `Init()` runs on the new tab. Each service view should be cheap to construct (no AWS calls in `New()`); the actual `DescribeX` happens in `Init()`'s returned command.

## Refresh key

`r` is reserved as the global "refresh active tab" key. Each tab implements it by re-issuing its load command and invalidating its cache entry (see `12-state-and-caching.md`).

## Acceptance criteria

- Header shows profile/region/service. Profile background colour matches the rule table.
- Tabs render with `[E]C2`, `[C]loudFront`, etc. Active tab is highlighted.
- Hotkeys `e/c/s/b/l` switch tabs immediately.
- `tab` / `shift+tab` cycle tabs.
- Window resize forwards to all tab models.
- Each tab's `Init()` runs only the first time it becomes active.
