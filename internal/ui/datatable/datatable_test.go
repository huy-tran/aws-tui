package datatable

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func newSample() Model {
	m := New([]Column{
		{Title: "Name", Width: 12},
		{Title: "State", Width: 8},
		{Title: "Type", Width: 10},
	})
	m.SetHeight(20)
	m.SetRows([]Row{
		{"web-01", "running", "t3.medium"},
		{"web-02", "stopped", "t3.medium"},
		{"db-01", "running", "m5.large"},
	})
	return m
}

func TestRenderHasBorders(t *testing.T) {
	out := newSample().View()
	// Box-drawing chars should be present on every horizontal edge.
	if !strings.Contains(out, "─") {
		t.Fatalf("expected horizontal border char in output, got:\n%s", out)
	}
	if !strings.Contains(out, "│") {
		t.Fatalf("expected vertical border char in output, got:\n%s", out)
	}
	if !strings.Contains(out, "web-01") || !strings.Contains(out, "db-01") {
		t.Fatalf("expected row content rendered, got:\n%s", out)
	}
}

func TestCursorMovesWithKeys(t *testing.T) {
	m := newSample()
	if m.Cursor() != 0 {
		t.Fatalf("expected cursor 0 at start, got %d", m.Cursor())
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.Cursor() != 1 {
		t.Fatalf("expected cursor 1 after down, got %d", m.Cursor())
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	// At end of 3 rows, cursor should clamp to 2.
	if m.Cursor() != 2 {
		t.Fatalf("expected cursor clamped to 2, got %d", m.Cursor())
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	if m.Cursor() != 0 {
		t.Fatalf("expected cursor 0 after home, got %d", m.Cursor())
	}
}

func TestSelectedRow(t *testing.T) {
	m := newSample()
	m.SetCursor(1)
	row := m.SelectedRow()
	if len(row) != 3 || row[0] != "web-02" {
		t.Fatalf("expected web-02 row selected, got %v", row)
	}
}

func TestBlurredIgnoresKeys(t *testing.T) {
	m := newSample()
	m.Blur()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.Cursor() != 0 {
		t.Fatalf("expected cursor to stay at 0 when blurred, got %d", m.Cursor())
	}
}

func TestFlexColumnExpandsToFitWidth(t *testing.T) {
	m := New([]Column{
		{Title: "Name", Flex: true},
		{Title: "State"},
		{Title: "Type"},
	})
	m.SetHeight(10)
	m.SetWidth(80)
	m.SetRows([]Row{
		{"web", "running", "t3.medium"},
	})
	out := m.View()
	// Each line of the rendered table should be exactly 80 chars wide.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, line := range lines {
		if got := runeWidth(line); got != 80 {
			t.Fatalf("line %d width %d, expected 80: %q", i, got, line)
		}
	}
}

func TestNoFlexExpandsFirstColumn(t *testing.T) {
	m := New([]Column{
		{Title: "A"},
		{Title: "B"},
	})
	m.SetHeight(8)
	m.SetWidth(40)
	m.SetRows([]Row{{"x", "y"}})
	out := m.View()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, line := range lines {
		if got := runeWidth(line); got != 40 {
			t.Fatalf("line %d width %d, expected 40: %q", i, got, line)
		}
	}
}

func TestColumnsAutoSizeToContent(t *testing.T) {
	// No width set: columns shrink to fit content + header.
	m := New([]Column{
		{Title: "Hello"},
		{Title: "X"},
	})
	m.SetHeight(8)
	m.SetRows([]Row{{"hi", "world"}})
	widths := m.computeContentWidths()
	if widths[0] != 5 { // "Hello" header is widest
		t.Fatalf("col 0 width = %d, want 5", widths[0])
	}
	if widths[1] != 5 { // "world" cell is widest
		t.Fatalf("col 1 width = %d, want 5", widths[1])
	}
}

// runeWidth counts terminal cells; we don't have wide chars in fixtures so
// rune count is correct enough for these tests after stripping ANSI codes.
func runeWidth(s string) int {
	s = stripANSI(s)
	return len([]rune(s))
}

func TestSortByColumnAscDesc(t *testing.T) {
	m := New([]Column{
		{Title: "Name"},
		{Title: "Count", SortAs: SortNumeric},
	})
	m.SetHeight(20)
	m.SetRows([]Row{
		{"charlie", "30"},
		{"alpha", "20"},
		{"bravo", "10"},
	})
	// Press 's' then '1' -> sort by Name asc.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if !m.sortPicking {
		t.Fatalf("expected sort ribbon to be active")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.sortPicking {
		t.Fatalf("expected sort ribbon to close after column pick")
	}
	if got := m.rows[0][0]; got != "alpha" {
		t.Fatalf("expected alpha first after asc-by-Name, got %q", got)
	}
	// Toggle: 's' '1' again -> desc.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if got := m.rows[0][0]; got != "charlie" {
		t.Fatalf("expected charlie first after desc-by-Name, got %q", got)
	}
}

func TestSortByNumericColumn(t *testing.T) {
	m := New([]Column{
		{Title: "Name"},
		{Title: "Count", SortAs: SortNumeric},
	})
	m.SetHeight(20)
	m.SetRows([]Row{
		{"a", "100"},
		{"b", "9"},
		{"c", "55"},
	})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	got := []string{m.rows[0][1], m.rows[1][1], m.rows[2][1]}
	want := []string{"9", "55", "100"}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("numeric sort mismatch at %d: got %v want %v", i, got, want)
		}
	}
}

func TestSortByTimeColumnWithZone(t *testing.T) {
	m := New([]Column{
		{Title: "Name"},
		{Title: "Created", SortAs: SortTime},
	})
	m.SetHeight(20)
	// Displayed timestamps carry a trailing local-zone abbreviation (and the
	// abbreviation can differ across DST). Sorting must order them by the
	// wall-clock time, not lexically by the whole string.
	m.SetRows([]Row{
		{"a", "2026-06-08 10:00:00 AEST"},
		{"b", "2026-06-08 09:00:00 AEST"},
		{"c", "2026-06-08 11:00:00 AEDT"},
	})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	got := []string{m.rows[0][0], m.rows[1][0], m.rows[2][0]}
	want := []string{"b", "a", "c"}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("time sort mismatch at %d: got %v want %v", i, got, want)
		}
	}
}

func TestSortCancelWithEsc(t *testing.T) {
	m := New([]Column{{Title: "Name"}})
	m.SetHeight(10)
	m.SetRows([]Row{{"a"}, {"b"}})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.sortPicking {
		t.Fatalf("esc should close the ribbon")
	}
	if m.sortCol != -1 {
		t.Fatalf("esc should not change sort column, got %d", m.sortCol)
	}
}

func TestStyledCellSurvivesWhenItFits(t *testing.T) {
	// Reproduces the bug where ANSI-coloured cells were silently truncated
	// mid-escape because the old truncate compared rune count to width.
	// "● Green" is 7 cells visible; with the ANSI escapes it's ~20 runes.
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("34")).Render("●") + " Green"
	m := New([]Column{{Title: "Health"}})
	m.SetHeight(10)
	m.SetRows([]Row{{styled}})
	out := m.View()
	if !strings.Contains(out, "Green") {
		t.Fatalf("styled cell text was lost in render:\n%s", out)
	}
}

func TestEmptyRowsRendersHeaderOnly(t *testing.T) {
	m := New([]Column{{Title: "A", Width: 6}, {Title: "B", Width: 6}})
	m.SetHeight(10)
	out := m.View()
	if !strings.Contains(out, "A") || !strings.Contains(out, "B") {
		t.Fatalf("expected header titles in output, got:\n%s", out)
	}
}
