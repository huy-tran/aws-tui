package securityhub

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func findingsModel() Model {
	m := testModel()
	m.mode = modeFindings
	updated, _ := m.Update(findingsLoadedMsg{
		scope: "all active",
		items: []Finding{
			{ID: "a", Title: "alpha", Severity: "HIGH"},
			{ID: "b", Title: "bravo", Severity: "LOW"},
		},
	})
	return updated.(Model)
}

// The findings list used to bind 's' to the suppressed toggle, which stopped
// the key ever reaching findingsTable - so the globally advertised
// "s then 1..N" sort did nothing here. The toggle now lives on 'x'.

func TestSortKeyReachesFindingsTable(t *testing.T) {
	m := findingsModel()
	before := m.showSuppress

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = updated.(Model)

	if m.showSuppress != before {
		t.Fatalf("'s' must no longer toggle suppressed visibility")
	}
	if !m.findingsTable.CapturingInput() {
		t.Fatalf("'s' should reach findingsTable and open the sort-column picker")
	}
}

func TestSuppressedToggleMovedToX(t *testing.T) {
	m := findingsModel()
	before := m.showSuppress

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(Model)

	if m.showSuppress == before {
		t.Fatalf("'x' should toggle suppressed visibility")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(Model)
	if m.showSuppress != before {
		t.Fatalf("'x' should toggle back")
	}
}
