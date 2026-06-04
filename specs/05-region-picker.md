# 05 — Region Picker

Shown after profile selection. Defaults to the profile's configured region (if any) or the last-used region for that profile.

> **Update — table-based picker.** The picker now renders with the shared
> `internal/ui/datatable` component instead of `bubbles/list`, matching the
> profile picker and every other list surface. Columns: `Region` / `Name`
> (flex) / `Status` (the opt-in badge from the `DescribeRegions` probe). The
> screen is a `header` line, a `/`-activated filter line, the bordered table,
> then a help line, with blank spacer rows around the table. `enter` selects
> the highlighted region, `+` opens the custom-region input, `esc` pops back.
> The `bubbles/list` sample below is retained for historical context only.

## Layout

```
┌─ Select Region (profile: prod-client-a) ──────────────────┐
│                                                           │
│  Filter: ___________________________________              │
│                                                           │
│ > ap-southeast-2    Sydney                                │
│   ap-southeast-1    Singapore                             │
│   ap-southeast-4    Melbourne                             │
│   us-east-1         N. Virginia                           │
│   us-east-2         Ohio                                  │
│   us-west-2         Oregon                                │
│   eu-west-1         Ireland                               │
│   eu-west-2         London                                │
│   eu-central-1      Frankfurt                             │
│                                                           │
│   [+] Custom region                                       │
│                                                           │
│  ↑/↓ navigate   enter select   esc back                   │
└───────────────────────────────────────────────────────────┘
```

## Implementation (internal/views/region/region.go)

```go
package region

import (
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/YOUR_USERNAME/aws-tui/internal/aws"
	"github.com/YOUR_USERNAME/aws-tui/internal/app"
	"github.com/YOUR_USERNAME/aws-tui/internal/state"
)

type regionItem struct {
	code string
	name string
}

func (r regionItem) Title() string       { return r.code }
func (r regionItem) Description() string { return r.name }
func (r regionItem) FilterValue() string { return r.code + " " + r.name }

var regionNames = map[string]string{
	"ap-southeast-2": "Sydney",
	"ap-southeast-1": "Singapore",
	"ap-southeast-4": "Melbourne",
	"ap-northeast-1": "Tokyo",
	"us-east-1":      "N. Virginia",
	"us-east-2":      "Ohio",
	"us-west-1":      "N. California",
	"us-west-2":      "Oregon",
	"eu-west-1":      "Ireland",
	"eu-west-2":      "London",
	"eu-central-1":   "Frankfurt",
}

type Model struct {
	ctx        *awspkg.Context
	state      *state.Store
	list       list.Model
	customMode bool
	custom     textinput.Model
}

func New(ctx *awspkg.Context, store *state.Store) Model {
	items := make([]list.Item, 0, len(awspkg.CommonRegions))
	for _, code := range awspkg.CommonRegions {
		items = append(items, regionItem{code: code, name: regionNames[code]})
	}

	delegate := list.NewDefaultDelegate()
	l := list.New(items, delegate, 0, 0)
	l.Title = "Select Region (profile: " + ctx.Profile + ")"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)

	// Pre-select last used region for this profile
	lastRegion := store.LastRegionFor(ctx.Profile)
	if lastRegion != "" {
		for i, item := range items {
			if ri, ok := item.(regionItem); ok && ri.code == lastRegion {
				l.Select(i)
				break
			}
		}
	}

	ti := textinput.New()
	ti.Placeholder = "e.g. me-south-1"
	ti.CharLimit = 20

	return Model{ctx: ctx, state: store, list: l, custom: ti}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width-4, msg.Height-4)
		return m, nil

	case tea.KeyMsg:
		if m.customMode {
			switch msg.String() {
			case "enter":
				region := m.custom.Value()
				if region != "" {
					return m, m.selectRegion(region)
				}
			case "esc":
				m.customMode = false
				return m, nil
			}
			var cmd tea.Cmd
			m.custom, cmd = m.custom.Update(msg)
			return m, cmd
		}

		if m.list.FilterState() == list.Filtering {
			break
		}
		switch msg.String() {
		case "enter":
			if it, ok := m.list.SelectedItem().(regionItem); ok {
				return m, m.selectRegion(it.code)
			}
		case "+":
			m.customMode = true
			m.custom.Focus()
			return m, textinput.Blink
		case "esc":
			return m, app.PopView()
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) selectRegion(region string) tea.Cmd {
	m.state.SetLastRegionFor(m.ctx.Profile, region)
	_ = m.state.Save()
	return func() tea.Msg {
		return app.RegionSelectedMsg{Region: region}
	}
}

func (m Model) View() string {
	if m.customMode {
		return lipgloss.NewStyle().Padding(1, 2).Render(
			"Enter region code:\n\n" + m.custom.View() + "\n\nenter to confirm · esc to cancel",
		)
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(m.list.View())
}
```

## Behaviour notes

- Pre-selection priority: per-profile last region > profile's configured `region` from `~/.aws/config` > top of list.
- Per-profile region memory is important: `prod-client-a` is always Sydney, `client-b` is always us-east-1, etc.
- `+` enters custom-region mode for less common regions.
- `esc` goes back to the profile picker (pop view).

## Acceptance criteria

- Common regions list renders with both code and human name.
- Last region used for this specific profile is pre-selected.
- `+` allows typing a custom region.
- `enter` emits `RegionSelectedMsg` and persists the per-profile last region.
- `esc` pops back to profile picker.
