package profile

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/state"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
)

type Model struct {
	table      datatable.Model
	filter     textinput.Model
	filterMode bool
	state      *state.Store
	profiles   []awspkg.Profile // full list as returned by ListProfiles
	displayed  []awspkg.Profile // visible after filter; equals profiles when no filter
	width      int
	height     int
	err        error
}

func New(store *state.Store) Model {
	profiles, err := awspkg.ListProfiles()

	cols := []datatable.Column{
		{Title: "Profile", Flex: true},
		{Title: "Source"},
		{Title: "Default Region"},
	}
	t := datatable.New(cols)
	t.SetHeight(20)

	flt := textinput.New()
	flt.Placeholder = "filter by name / source / region"
	flt.CharLimit = 64
	flt.Prompt = "/ "

	m := Model{
		table:    t,
		filter:   flt,
		state:    store,
		profiles: profiles,
		err:      err,
	}
	m.applyFilter()
	// Pre-select the last-used profile so enter re-enters it with no scrolling.
	if store.LastProfile != "" {
		for i, p := range m.displayed {
			if p.Name == store.LastProfile {
				m.table.SetCursor(i)
				break
			}
		}
	}
	return m
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 6
		if h < 3 {
			h = 3
		}
		m.table.SetHeight(h)
		m.table.SetWidth(msg.Width)
		return m, nil

	case tea.KeyMsg:
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
		case "/":
			m.filterMode = true
			m.filter.Focus()
			return m, textinput.Blink
		case "enter":
			if p := m.selected(); p != nil {
				m.state.LastProfile = p.Name
				_ = m.state.Save()
				name := p.Name
				return m, func() tea.Msg {
					return nav.ProfileSelectedMsg{Profile: name}
				}
			}
		}
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// applyFilter rebuilds m.displayed and the table rows from the current filter
// text. Always allocates a fresh slice so it never aliases m.profiles.
func (m *Model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	out := make([]awspkg.Profile, 0, len(m.profiles))
	for _, p := range m.profiles {
		if q != "" && !profileMatches(p, q) {
			continue
		}
		out = append(out, p)
	}
	m.displayed = out
	rows := make([]datatable.Row, len(out))
	for i, p := range out {
		rows[i] = datatable.Row{p.Name, p.Source, defaultStr(p.Region, "-")}
	}
	m.table.SetRows(rows)
}

func profileMatches(p awspkg.Profile, q string) bool {
	return strings.Contains(strings.ToLower(p.Name), q) ||
		strings.Contains(strings.ToLower(p.Source), q) ||
		strings.Contains(strings.ToLower(p.Region), q)
}

func (m Model) selected() *awspkg.Profile {
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.displayed) {
		return nil
	}
	return &m.displayed[idx]
}

// CapturingInput tells the app whether a focused input is consuming text so
// global keys like '?' aren't swallowed.
func (m Model) CapturingInput() bool {
	return m.filterMode || m.table.CapturingInput()
}

func (m Model) View() string {
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf("Error loading profiles: %v\n\nIs ~/.aws/config present?", m.err))
	}
	if len(m.profiles) == 0 {
		return mutedStyle.Render(
			"No AWS profiles found.\n\n" +
				"Run `aws configure` or `aws configure sso` to create one,\n" +
				"then restart this tool.\n\n" +
				"Press ctrl+c to quit.",
		)
	}
	header := headerStyle.Render(fmt.Sprintf("Select AWS Profile (%d)", len(m.profiles)))
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}
	body := m.table.View()
	if len(m.displayed) == 0 {
		body = mutedStyle.Render("No profiles match filter.")
	}
	help := mutedStyle.Render("enter: select · /: filter · ↑/↓ j/k: move · s: sort")
	return lipgloss.JoinVertical(lipgloss.Left, header, filterLine, "", body, "", help)
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

var (
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle = lipgloss.NewStyle().Bold(true)
)
