package dashboard

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// stubTab is a minimal tea.Model used to drive Update() in isolation. It
// records every key it receives and optionally reports CapturingInput /
// InSubnav so we can exercise every routing branch in the dashboard.
type stubTab struct {
	id        string
	capturing bool
	subnav    bool
	keys      []string
}

func (s *stubTab) Init() tea.Cmd { return nil }

func (s *stubTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		s.keys = append(s.keys, k.String())
	}
	return s, nil
}

func (s *stubTab) View() string { return s.id }

func (s *stubTab) CapturingInput() bool { return s.capturing }

func (s *stubTab) InSubnav() bool { return s.subnav }

func keyMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// newTestModel builds a Model wired up with stub tabs in every slot. ctx is
// left nil because Update() never touches it. Stubs are ordered to match
// the Tab constants in dashboard.go so stubs[TabX] is the right stub for X.
func newTestModel() (Model, [8]*stubTab) {
	stubs := [8]*stubTab{
		{id: "beanstalk"},
		{id: "ec2"},
		{id: "rds"},
		{id: "logs"},
		{id: "cloudfront"},
		{id: "s3"},
		{id: "paramstore"},
		{id: "securityhub"},
	}
	m := Model{active: TabBeanstalk}
	for i, s := range stubs {
		m.tabs[i] = s
	}
	return m, stubs
}

func TestLettersAlwaysDelegateToActiveTab(t *testing.T) {
	// Letter hotkeys for tab switching are gone - most letters reach the
	// active view. 'b' and 'B' are now reserved by the dashboard for the
	// bookmarks workflow so they don't propagate.
	m, stubs := newTestModel()
	for _, k := range []string{"e", "c", "s", "l", "r", "/", "x"} {
		updated, _ := m.Update(keyMsg(k))
		m = updated.(Model)
	}
	if m.active != TabBeanstalk {
		t.Fatalf("expected active to stay TabBeanstalk, got %v", m.active)
	}
	if got, want := len(stubs[TabBeanstalk].keys), 7; got != want {
		t.Fatalf("expected %d keys delivered to beanstalk stub, got %d (%v)", want, got, stubs[TabBeanstalk].keys)
	}
}

func TestArrowKeysSwitchTabsOnRootList(t *testing.T) {
	m, _ := newTestModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := updated.(Model).active; got != TabEC2 {
		t.Fatalf("expected right to advance to TabEC2, got %v", got)
	}
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyLeft})
	if got := updated.(Model).active; got != TabBeanstalk {
		t.Fatalf("expected left to return to TabBeanstalk, got %v", got)
	}
}

func TestArrowKeysPassThroughWhileCapturing(t *testing.T) {
	m, stubs := newTestModel()
	stubs[TabBeanstalk].capturing = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := updated.(Model).active; got != TabBeanstalk {
		t.Fatalf("expected active to stay TabBeanstalk while capturing, got %v", got)
	}
	if len(stubs[TabBeanstalk].keys) != 1 || stubs[TabBeanstalk].keys[0] != "right" {
		t.Fatalf("expected 'right' delivered to active tab, got %v", stubs[TabBeanstalk].keys)
	}
}

func TestArrowKeysPassThroughInSubnav(t *testing.T) {
	m, stubs := newTestModel()
	stubs[TabBeanstalk].subnav = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := updated.(Model).active; got != TabBeanstalk {
		t.Fatalf("expected active to stay TabBeanstalk while in subnav, got %v", got)
	}
	if len(stubs[TabBeanstalk].keys) != 1 || stubs[TabBeanstalk].keys[0] != "right" {
		t.Fatalf("expected 'right' delivered to active tab, got %v", stubs[TabBeanstalk].keys)
	}
}

func TestTabKeyAlwaysSwitchesAsEscapeHatch(t *testing.T) {
	m, stubs := newTestModel()
	stubs[TabBeanstalk].capturing = true
	stubs[TabBeanstalk].subnav = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := updated.(Model).active; got != TabEC2 {
		t.Fatalf("expected tab to switch tabs as escape hatch, got active=%v", got)
	}
}

func TestShiftTabSwitchesBackward(t *testing.T) {
	m, _ := newTestModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := updated.(Model).active; got != TabSecurityHub {
		t.Fatalf("expected shift+tab from TabBeanstalk to wrap to TabSecurityHub, got %v", got)
	}
}
