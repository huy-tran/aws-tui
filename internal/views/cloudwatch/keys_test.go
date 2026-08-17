package cloudwatch

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The log groups view used to bind 's' to search, which meant the key never
// reached groupsTable and the "s then 1..N" sort advertised in the global help
// silently did nothing here. Search now lives on 'S'.

func TestSortKeyReachesGroupsTable(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = updated.(Model)

	if m.mode != modeGroups {
		t.Fatalf("'s' must not leave modeGroups, got mode %v", m.mode)
	}
	if !m.groupsTable.CapturingInput() {
		t.Fatalf("'s' should reach groupsTable and open the sort-column picker")
	}
}

func TestSearchMovedToShiftS(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(groupsLoadedMsg{items: []LogGroup{{Name: "/aws/lambda/one"}}})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	m = updated.(Model)
	if m.mode != modeSearch {
		t.Fatalf("'S' should open search, got mode %v", m.mode)
	}
}

// Refresh moved from 'r' to ctrl+r across every view. 'r' is a printable rune
// and so was swallowed whenever a filter or form field had focus.

func TestCtrlRRefreshesGroups(t *testing.T) {
	m := testModel()
	key := "logs:groups:" + m.ctx.Region
	m.ctx.Cache.Set(key, []LogGroup{{Name: "stale"}}, time.Minute)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = updated.(Model)

	if _, ok := m.ctx.Cache.Get(key); ok {
		t.Fatalf("ctrl+r should invalidate the cached groups list")
	}
	if !m.groupsLoading {
		t.Fatalf("ctrl+r should put the view into its loading state")
	}
	if cmd == nil {
		t.Fatalf("ctrl+r should return a reload command")
	}
}

func TestPlainRNoLongerRefreshes(t *testing.T) {
	m := testModel()
	key := "logs:groups:" + m.ctx.Region
	m.ctx.Cache.Set(key, []LogGroup{{Name: "keep"}}, time.Minute)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(Model)

	if _, ok := m.ctx.Cache.Get(key); !ok {
		t.Fatalf("plain 'r' must no longer invalidate the cache")
	}
}
