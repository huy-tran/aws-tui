// Package datatable renders a scrollable table with per-cell borders. It
// owns cursor + viewport offset state and exposes a small Bubble Tea-style
// surface (Update, View, SetRows, etc.) so tab views can swap it in where
// they used bubbles/table.
//
// Borders are drawn with lipgloss/table; the cursor row is highlighted via
// StyleFunc rather than by mutating the underlying data.
package datatable

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ltable "github.com/charmbracelet/lipgloss/table"

	"github.com/huy-tran/aws-tui/internal/ui/theme"
)

// SortKind controls how a column's values are compared. The zero value is
// SortString (lexicographic on the post-ANSI-strip text), which is good
// enough for names / ids / domains. Use SortNumeric for counts / sizes
// and SortTime for "2006-01-02 15:04:05" timestamps.
type SortKind int

const (
	SortString SortKind = iota
	SortNumeric
	SortTime
)

// Column describes a single column. Width is a soft minimum (0 = compute
// from content). Flex marks the column that absorbs leftover horizontal
// space when the table is sized to fit a terminal width; if no column is
// flagged Flex, the first column gets the extra room. Align controls
// horizontal alignment within the cell - the zero value (lipgloss.Left)
// keeps the existing left-aligned behaviour.
type Column struct {
	Title  string
	Width  int
	Flex   bool
	Align  lipgloss.Position
	SortAs SortKind
}

// Row is one record's worth of cell strings, in column order.
type Row []string

type Model struct {
	columns []Column
	rows    []Row
	cursor  int
	offset  int
	height  int
	width   int
	focused bool

	// Sort state. sortCol == -1 means rows are kept in the order the
	// caller passed; otherwise rows are sorted by that column with
	// sortDir == +1 (asc) or -1 (desc) on every SetRows.
	sortCol int
	sortDir int

	// sortPicking is true while the "press a column letter" ribbon is up.
	sortPicking bool
}

func New(cols []Column) Model {
	return Model{columns: cols, focused: true, sortCol: -1, sortDir: 1}
}

func (m *Model) SetColumns(cols []Column) { m.columns = cols }

func (m *Model) SetRows(rows []Row) {
	// Remember the cursor's logical row so we can restore it after sorting
	// reshuffles the order.
	var anchor Row
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		anchor = m.rows[m.cursor]
	}
	m.rows = rows
	m.applySort()
	if anchor != nil {
		for i, r := range m.rows {
			if rowsEqual(r, anchor) {
				m.cursor = i
				break
			}
		}
	}
	m.clamp()
}

func rowsEqual(a, b Row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// applySort sorts m.rows in place according to m.sortCol / m.sortDir.
// No-op when no column is selected.
func (m *Model) applySort() {
	if m.sortCol < 0 || m.sortCol >= len(m.columns) {
		return
	}
	col := m.sortCol
	kind := m.columns[col].SortAs
	dir := m.sortDir
	sort.SliceStable(m.rows, func(i, j int) bool {
		ai := cellAt(m.rows[i], col)
		aj := cellAt(m.rows[j], col)
		less := compareCells(ai, aj, kind)
		if dir < 0 {
			return !less && ai != aj
		}
		return less
	})
}

func cellAt(r Row, col int) string {
	if col >= 0 && col < len(r) {
		return stripANSI(r[col])
	}
	return ""
}

func compareCells(a, b string, kind SortKind) bool {
	switch kind {
	case SortNumeric:
		fa, oka := parseLeadingNumber(a)
		fb, okb := parseLeadingNumber(b)
		if oka && okb {
			return fa < fb
		}
	case SortTime:
		ta, oka := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(a))
		tb, okb := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(b))
		if oka == nil && okb == nil {
			return ta.Before(tb)
		}
	}
	return strings.ToLower(a) < strings.ToLower(b)
}

// parseLeadingNumber pulls the leading numeric prefix from s. "423"
// returns 423; "5d" returns 5; "1.2 G" returns 1.2; "abc" returns 0, false.
func parseLeadingNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	end := 0
	sawDigit := false
	for i, r := range s {
		if unicode.IsDigit(r) || r == '.' || (i == 0 && (r == '+' || r == '-')) {
			end = i + 1
			if unicode.IsDigit(r) {
				sawDigit = true
			}
			continue
		}
		break
	}
	if !sawDigit {
		return 0, false
	}
	f, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func (m *Model) SetHeight(h int) {
	m.height = h
	m.clamp()
}

// SetWidth sets the total table width including borders. The flex column
// (or column 0 if none is flagged) expands to consume leftover horizontal
// space. If w <= 0, columns use their content-derived minimums only.
func (m *Model) SetWidth(w int) { m.width = w }

func (m *Model) Focus()           { m.focused = true }
func (m *Model) Blur()            { m.focused = false }
func (m Model) Focused() bool     { return m.focused }
func (m Model) Cursor() int       { return m.cursor }
func (m Model) Rows() []Row       { return m.rows }
func (m Model) Height() int       { return m.height }
func (m Model) Width() int        { return m.width }
func (m Model) Columns() []Column { return m.columns }

func (m *Model) SetCursor(c int) {
	m.cursor = c
	m.clamp()
}

func (m *Model) GotoTop()    { m.cursor = 0; m.offset = 0 }
func (m *Model) GotoBottom() { m.cursor = len(m.rows) - 1; m.clamp() }

func (m Model) SelectedRow() Row {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor]
}

// visibleRows returns how many data rows fit at the current height. With
// per-cell row borders, n rows take 2n+3 lines (top border + header + header
// separator + N*(row + row-separator) - 1 + bottom border).
func (m Model) visibleRows() int {
	n := (m.height - 3) / 2
	if n < 1 {
		return 1
	}
	return n
}

func (m *Model) clamp() {
	if len(m.rows) == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	vis := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	} else if m.cursor >= m.offset+vis {
		m.offset = m.cursor - vis + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.focused {
		return m, nil
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.sortPicking {
		return m.handleSortPick(km), nil
	}
	switch km.String() {
	case "s":
		m.sortPicking = true
		return m, nil
	case "up", "k":
		m.cursor--
	case "down", "j":
		m.cursor++
	case "pgup", "ctrl+b":
		m.cursor -= m.visibleRows()
	case "pgdown", "ctrl+f", " ":
		m.cursor += m.visibleRows()
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = len(m.rows) - 1
	default:
		return m, nil
	}
	m.clamp()
	return m, nil
}

// handleSortPick consumes the next key while the sort ribbon is up. Digit
// 1..N picks that column; first-letter-of-title also picks; esc cancels.
// Selecting the already-active column toggles direction.
func (m Model) handleSortPick(km tea.KeyMsg) Model {
	defer func() { m.sortPicking = false }()
	s := km.String()
	if s == "esc" {
		m.sortPicking = false
		return m
	}
	target := -1
	if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(m.columns) {
		target = n - 1
	} else if len(s) == 1 {
		ch := strings.ToLower(s)
		for i, c := range m.columns {
			if len(c.Title) == 0 {
				continue
			}
			if strings.ToLower(string(c.Title[0])) == ch {
				target = i
				break
			}
		}
	}
	m.sortPicking = false
	if target < 0 {
		return m
	}
	if m.sortCol == target {
		m.sortDir = -m.sortDir
	} else {
		m.sortCol = target
		m.sortDir = 1
	}
	// Re-apply on the live rows so the table updates immediately.
	var anchor Row
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		anchor = m.rows[m.cursor]
	}
	m.applySort()
	if anchor != nil {
		for i, r := range m.rows {
			if rowsEqual(r, anchor) {
				m.cursor = i
				break
			}
		}
	}
	m.clamp()
	return m
}

// CapturingInput is true while the sort ribbon is up so the dashboard
// doesn't intercept the next keystroke.
func (m Model) CapturingInput() bool { return m.sortPicking }

// computeContentWidths returns the per-column content width (without padding
// or borders), expanded so the rendered table totals m.width when set.
func (m Model) computeContentWidths() []int {
	n := len(m.columns)
	if n == 0 {
		return nil
	}
	widths := make([]int, n)
	flexIdx := -1
	for j, c := range m.columns {
		widths[j] = lipgloss.Width(c.Title)
		if c.Width > widths[j] {
			widths[j] = c.Width
		}
		if c.Flex && flexIdx < 0 {
			flexIdx = j
		}
	}
	for _, row := range m.rows {
		for j, cell := range row {
			if j >= n {
				break
			}
			if w := lipgloss.Width(cell); w > widths[j] {
				widths[j] = w
			}
		}
	}
	if m.width <= 0 {
		return widths
	}

	// Each cell adds 2 chars of horizontal padding (1 each side); the table
	// adds n+1 vertical border characters when BorderColumn(true).
	overhead := 2*n + (n + 1)
	total := overhead
	for _, w := range widths {
		total += w
	}
	if total == m.width {
		return widths
	}
	target := flexIdx
	if target < 0 {
		target = 0
	}
	if total < m.width {
		widths[target] += m.width - total
	} else {
		// Over budget: trim the flex column, keeping a sane minimum tied to
		// the header so the column heading still fits.
		deficit := total - m.width
		minFlex := lipgloss.Width(m.columns[target].Title)
		if minFlex < 4 {
			minFlex = 4
		}
		if widths[target]-deficit >= minFlex {
			widths[target] -= deficit
		} else {
			widths[target] = minFlex
		}
	}
	return widths
}

func (m Model) View() string {
	widths := m.computeContentWidths()

	headers := make([]string, len(m.columns))
	for i, c := range m.columns {
		title := c.Title
		if i == m.sortCol {
			arrow := " ↑"
			if m.sortDir < 0 {
				arrow = " ↓"
			}
			title += arrow
		}
		headers[i] = title
	}

	vis := m.visibleRows()
	end := m.offset + vis
	if end > len(m.rows) {
		end = len(m.rows)
	}
	data := make([][]string, 0, end-m.offset)
	for i := m.offset; i < end; i++ {
		row := make([]string, len(m.columns))
		for j := range m.columns {
			if j < len(m.rows[i]) {
				row[j] = truncate(m.rows[i][j], widths[j])
			}
		}
		data = append(data, row)
	}
	selectedLocal := m.cursor - m.offset

	base := lipgloss.NewStyle().Padding(0, 1)
	header := base.Bold(true).Foreground(lipgloss.Color("252"))
	selected := base.Bold(true).Foreground(lipgloss.Color("213"))

	t := ltable.New().
		Border(lipgloss.NormalBorder()).
		BorderRow(true).
		BorderColumn(true).
		BorderStyle(lipgloss.NewStyle().Foreground(theme.Current().BorderFg)).
		Headers(headers...).
		Rows(data...).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := base
			if col >= 0 && col < len(widths) {
				s = s.Width(widths[col] + 2)
			}
			if col >= 0 && col < len(m.columns) {
				s = s.Align(m.columns[col].Align)
			}
			switch {
			case row == ltable.HeaderRow:
				return header.Width(s.GetWidth()).Align(s.GetAlign())
			case row == selectedLocal:
				return selected.Width(s.GetWidth()).Align(s.GetAlign())
			default:
				return s
			}
		})

	body := t.Render()
	if m.sortPicking {
		return m.renderSortRibbon() + "\n" + body
	}
	return body
}

// renderSortRibbon shows the column letters / digits the user can press.
func (m Model) renderSortRibbon() string {
	var parts []string
	for i, c := range m.columns {
		parts = append(parts, fmt.Sprintf("[%d] %s", i+1, c.Title))
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	mutedRibbon := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	return style.Render("sort by: ") + strings.Join(parts, "  ") +
		mutedRibbon.Render("   (esc to cancel)")
}

// truncate shortens s to fit w terminal cells, appending an ellipsis if the
// input was trimmed. Visible width is what matters: an ANSI-coloured string
// has many more runes than terminal cells, so a rune-count compare would
// trigger truncation that chops mid-escape and produces a broken or empty
// cell. When truncation is genuinely needed we drop the styling rather than
// risk slicing inside an escape sequence.
func truncate(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	plain := stripANSI(s)
	runes := []rune(plain)
	if len(runes) <= w {
		return plain
	}
	if w == 1 {
		return string(runes[:1])
	}
	return string(runes[:w-1]) + "…"
}

// stripANSI removes CSI escape sequences (the "\x1b[...m" colour / style
// codes lipgloss emits). It is deliberately simple - it covers the subset
// of escapes lipgloss produces and is only used as a fallback when a styled
// cell genuinely needs to be shortened.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
