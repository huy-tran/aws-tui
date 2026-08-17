package beanstalk

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// eventsModel puts the view on the scrollable events screen with more content
// than fits, so paging and jumping actually move the offset.
func eventsModel() Model {
	m := testModel()
	m.mode = modeEvents
	m.eventsVP.Width = 80
	m.eventsVP.Height = 5
	m.eventsVP.SetContent(strings.Repeat("event line\n", 100))
	return m
}

// bubbles' viewport has no top/bottom binding at all, so g/G worked on every
// table but on none of the scrolling screens. internal/ui/scroll closes that.

func TestViewportJumpToBottomAndTop(t *testing.T) {
	m := eventsModel()
	if m.eventsVP.YOffset != 0 {
		t.Fatalf("expected to start at the top, got offset %d", m.eventsVP.YOffset)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	m = updated.(Model)
	bottom := m.eventsVP.YOffset
	if bottom == 0 {
		t.Fatalf("'G' should jump to the bottom of the events viewport")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	m = updated.(Model)
	if m.eventsVP.YOffset != 0 {
		t.Fatalf("'g' should jump back to the top, got offset %d", m.eventsVP.YOffset)
	}
}

// datatable pages on ctrl+b / ctrl+f; the stock viewport keymap only had b / f.
// scroll.KeyMap adds the chords without dropping the pager keys.

func TestViewportPagesWithCtrlF(t *testing.T) {
	m := eventsModel()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = updated.(Model)
	if m.eventsVP.YOffset == 0 {
		t.Fatalf("ctrl+f should page the events viewport down")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = updated.(Model)
	if m.eventsVP.YOffset != 0 {
		t.Fatalf("ctrl+b should page back to the top, got offset %d", m.eventsVP.YOffset)
	}
}

func TestEventsStillPageWithStockKeys(t *testing.T) {
	m := eventsModel()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = updated.(Model)
	if m.eventsVP.YOffset == 0 {
		t.Fatalf("the stock 'f' page-down key should still work")
	}
}
