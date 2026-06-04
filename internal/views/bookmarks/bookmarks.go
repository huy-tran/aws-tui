// Package bookmarks renders the bookmarks list pushed onto the app stack
// when the user presses 'B' from any tab's root list.
package bookmarks

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/state"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/statusbar"
)

// ChosenMsg is emitted when the user picks a bookmark with enter. The
// app intercepts it: pops the bookmarks view and forwards the msg to
// the dashboard which switches tabs and pre-fills the filter.
type ChosenMsg struct {
	Tab   string
	ID    string
	Label string
}

type Model struct {
	store   *state.Store
	profile string
	region  string
	table   datatable.Model
	items   []state.Bookmark
	status  string
	width   int
	height  int
}

func New(store *state.Store, profile, region string) Model {
	cols := []datatable.Column{
		{Title: "Tab"},
		{Title: "Region"},
		{Title: "Label", Flex: true},
	}
	t := datatable.New(cols)
	t.SetHeight(20)
	m := Model{store: store, profile: profile, region: region, table: t}
	m.refresh()
	return m
}

func (m *Model) refresh() {
	m.items = m.store.ListBookmarks(m.profile)
	rows := make([]datatable.Row, len(m.items))
	for i, b := range m.items {
		rows[i] = datatable.Row{b.Tab, b.Region, b.Label}
	}
	m.table.SetRows(rows)
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
		switch msg.String() {
		case "esc":
			return m, nav.PopView()
		case "enter":
			if b := m.selected(); b != nil {
				cmd := func() tea.Msg {
					return ChosenMsg{Tab: b.Tab, ID: b.ID, Label: b.Label}
				}
				return m, cmd
			}
		case "d":
			if b := m.selected(); b != nil {
				if m.store.RemoveBookmark(m.profile, b.Tab, b.ID) {
					_ = m.store.Save()
					m.status = "removed " + b.Label
					m.refresh()
				}
				return m, nil
			}
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m Model) selected() *state.Bookmark {
	if len(m.items) == 0 {
		return nil
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.items) {
		return nil
	}
	return &m.items[idx]
}

func (m Model) View() string {
	title := lipgloss.NewStyle().Bold(true).Render(
		fmt.Sprintf("Bookmarks (%d) · profile %s", len(m.items), m.profile))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	body := m.table.View()
	if len(m.items) == 0 {
		body = muted.Render("No bookmarks yet. Press 'b' on a row in any tab to add one.")
	}
	help := muted.Render("enter: open · d: delete · esc: back")
	parts := []string{title, "", body, "", help}
	if m.status != "" {
		parts = append(parts, muted.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// HelpItems contributes to the '?' overlay.
func (m Model) HelpItems() []help.Section {
	return []help.Section{{
		Title: "Bookmarks",
		Items: []help.Item{
			{Keys: "enter", Desc: "open bookmark (switches tab + filter)"},
			{Keys: "d", Desc: "delete bookmark"},
			{Keys: "esc", Desc: "back"},
			{Keys: "↑/↓ j/k", Desc: "move cursor"},
		},
	}}
}

func (m Model) StatusFooter() statusbar.Snapshot {
	return statusbar.Snapshot{
		View:    "Bookmarks",
		Profile: m.profile,
		Region:  m.region,
		Items:   len(m.items),
		Message: m.status,
	}
}
