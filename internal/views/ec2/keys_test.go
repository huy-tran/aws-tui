package ec2

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
)

func testCtx() *awspkg.Context {
	return &awspkg.Context{Profile: "test", Region: "us-east-1", Cache: awspkg.NewCache()}
}

// consoleWithOutput returns the console sub-view holding more lines than fit,
// so scrolling actually moves the offset.
func consoleWithOutput() consoleModel {
	m := newConsole(testCtx(), Instance{ID: "i-0123456789abcdef0"})
	m.loading = false
	m.output = strings.Repeat("boot line\n", 100)
	m.vp.Width = 80
	m.vp.Height = 5
	m.vp.SetContent(m.output)
	return m
}

// 'q' used to pop the view on exactly two screens (EC2 console and EC2
// details) and nowhere else. esc is the single back key now.

func TestConsoleQNoLongerPopsView(t *testing.T) {
	m := consoleWithOutput()

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if isPopView(cmd) {
		t.Fatalf("'q' should no longer pop the console view")
	}
}

func TestConsoleEscStillPopsView(t *testing.T) {
	m := consoleWithOutput()

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !isPopView(cmd) {
		t.Fatalf("esc should still pop the console view")
	}
}

func TestDetailsQNoLongerPopsView(t *testing.T) {
	m := newDetails(testCtx(), Instance{ID: "i-0123456789abcdef0"})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if isPopView(cmd) {
		t.Fatalf("'q' should no longer pop the details view")
	}
}

// The console viewport had no jump-to-bottom binding before scroll.Jump.

func TestConsoleViewportJump(t *testing.T) {
	m := consoleWithOutput()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	m = updated.(consoleModel)
	if m.vp.YOffset == 0 {
		t.Fatalf("'G' should jump to the bottom of the console output")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	m = updated.(consoleModel)
	if m.vp.YOffset != 0 {
		t.Fatalf("'g' should jump back to the top, got offset %d", m.vp.YOffset)
	}
}

// EC2's port-forward form is the one RDS was aligned to, so lock its submit
// key in as well.

func TestPortForwardSubmitsOnEnter(t *testing.T) {
	m := newPortForward(testCtx(), Instance{ID: "i-0123456789abcdef0"})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pf := updated.(portForwardModel)

	// Ports are empty, so startCmd fails validation - which still proves
	// enter ran the handler rather than being typed into a field.
	if pf.errMsg == "" && !pf.running {
		t.Fatalf("enter should submit the port-forward form")
	}
}

func TestCtrlRRefreshesInstances(t *testing.T) {
	m := New(testCtx())
	key := "ec2:instances:" + m.ctx.Region
	m.ctx.Cache.Set(key, []Instance{{ID: "stale"}}, time.Minute)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = updated.(Model)

	if _, ok := m.ctx.Cache.Get(key); ok {
		t.Fatalf("ctrl+r should invalidate the cached instance list")
	}
	if !m.loading {
		t.Fatalf("ctrl+r should put the view into its loading state")
	}
	if cmd == nil {
		t.Fatalf("ctrl+r should return a reload command")
	}
}

// isPopView reports whether cmd resolves to nav's pop-view message.
func isPopView(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(nav.PopViewMsg)
	return ok
}
