package profile

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/state"
)

type profileItem struct {
	profile awspkg.Profile
}

func (p profileItem) Title() string { return p.profile.Name }

func (p profileItem) Description() string {
	return fmt.Sprintf("%s · %s", p.profile.Source, defaultStr(p.profile.Region, "no default region"))
}

func (p profileItem) FilterValue() string { return p.profile.Name }

type Model struct {
	list     list.Model
	state    *state.Store
	profiles []awspkg.Profile
	width    int
	height   int
	err      error
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

	if store.LastProfile != "" {
		for i, item := range items {
			if pi, ok := item.(profileItem); ok && pi.profile.Name == store.LastProfile {
				l.Select(i)
				break
			}
		}
	}

	return Model{list: l, state: store, profiles: profiles, err: err}
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
					return nav.ProfileSelectedMsg{Profile: it.profile.Name}
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
	if len(m.profiles) == 0 {
		return lipgloss.NewStyle().Padding(1, 2).Render(
			"No AWS profiles found.\n\n" +
				"Run `aws configure` or `aws configure sso` to create one,\n" +
				"then restart this tool.\n\n" +
				"Press ctrl+c to quit.",
		)
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(m.list.View())
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
