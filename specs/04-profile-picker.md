# 04 — Profile Picker

The first screen. Shows the list of profiles from `~/.aws/config`, fuzzy-filterable, with metadata.

## Layout

```
┌─ Select AWS Profile ───────────────────────────────────────┐
│                                                            │
│  Filter: ___________________________________               │
│                                                            │
│ > prod-acme           SSO          ap-southeast-2          │
│   prod-widgets        SSO          us-east-1               │
│   staging-acme        assume-role  ap-southeast-2          │
│   personal            static       us-west-2               │
│                                                            │
│                                                            │
│  ↑/↓ navigate   enter select   / filter   ctrl+c quit      │
└────────────────────────────────────────────────────────────┘
```

Last used profile (from state) is pre-selected at top of cursor.

## Implementation (internal/views/profile/profile.go)

```go
package profile

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/YOUR_USERNAME/aws-tui/internal/aws"
	"github.com/YOUR_USERNAME/aws-tui/internal/app"
	"github.com/YOUR_USERNAME/aws-tui/internal/state"
)

type profileItem struct {
	profile awspkg.Profile
}

func (p profileItem) Title() string       { return p.profile.Name }
func (p profileItem) Description() string {
	return fmt.Sprintf("%s · %s", p.profile.Source, defaultStr(p.profile.Region, "no default region"))
}
func (p profileItem) FilterValue() string { return p.profile.Name }

type Model struct {
	list   list.Model
	state  *state.Store
	width  int
	height int
	err    error
}

func New(store *state.Store) Model {
	profiles, err := awspkg.ListProfiles()

	items := make([]list.Item, len(profiles))
	for i, p := range profiles {
		items[i] = profileItem{profile: p}
	}

	delegate := list.NewDefaultDelegate()
	l := list.New(items, delegate, 0, 0)
	l.Title = "Select AWS Profile"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(true)

	// Pre-select last used
	if store.LastProfile != "" {
		for i, item := range items {
			if pi, ok := item.(profileItem); ok && pi.profile.Name == store.LastProfile {
				l.Select(i)
				break
			}
		}
	}

	return Model{list: l, state: store, err: err}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width-4, msg.Height-4)
		return m, nil

	case tea.KeyMsg:
		if m.list.FilterState() == list.Filtering {
			break
		}
		switch msg.String() {
		case "enter":
			if it, ok := m.list.SelectedItem().(profileItem); ok {
				m.state.LastProfile = it.profile.Name
				_ = m.state.Save()
				return m, func() tea.Msg {
					return app.ProfileSelectedMsg{Profile: it.profile.Name}
				}
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	if m.err != nil {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("196")).
			Render(fmt.Sprintf("Error loading profiles: %v\n\nIs ~/.aws/config present?", m.err))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(m.list.View())
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
```

## Custom item rendering (optional polish)

The default `list.DefaultDelegate` shows title + description on two lines. If you want a single-line table-style layout (better for many profiles), implement a custom delegate. For v1, default delegate is fine.

## Empty state

If `ListProfiles()` returns zero profiles, show:

```
No AWS profiles found.

Run `aws configure` or `aws configure sso` to create one,
then restart this tool.

Press ctrl+c to quit.
```

## Behaviour notes

- Filtering is built into `bubbles/list`. Press `/` to enter filter mode, type, `esc` to clear.
- The list reads profiles **once** on creation. If the user adds a profile externally, they need to restart (or hit a refresh key — defer to v2).
- We do NOT validate credentials on this screen. Just show what's in the config files. Validation happens after region selection.

## Acceptance criteria

- Launching the app shows all profiles from `~/.aws/config` and `~/.aws/credentials`.
- Last-used profile (persisted state) is the selected row on launch.
- `/` filters the list; `esc` clears.
- `enter` emits `ProfileSelectedMsg` and persists the selection.
- If no profiles exist, an informative message is shown.
