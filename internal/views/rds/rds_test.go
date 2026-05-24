package rds

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
)

func testModel() Model {
	return New(&awspkg.Context{Profile: "test", Region: "us-east-1", Cache: awspkg.NewCache()})
}

func TestRDSFilterTransitions(t *testing.T) {
	m := testModel()
	if m.CapturingInput() {
		t.Fatalf("starts not capturing")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = updated.(Model)
	if !m.CapturingInput() {
		t.Fatalf("/ should open filter")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.CapturingInput() {
		t.Fatalf("esc should close filter")
	}
}

func TestRDSYankPendingState(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(loadedMsg{items: []DBInstance{{ID: "db-1", Endpoint: "h.example.com", Port: 5432}}})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	if !m.CapturingInput() {
		t.Fatalf("'y' should put view into yank-pending (capturing)")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	m = updated.(Model)
	if m.CapturingInput() {
		t.Fatalf("follow-up key should clear yank-pending")
	}
}

func TestRDSPortForwardModalOpens(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(loadedMsg{items: []DBInstance{{ID: "db-1", Endpoint: "h.example.com", Port: 5432}}})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = updated.(Model)
	if m.mode != modePortForward {
		t.Fatalf("'f' should open modePortForward, got %v", m.mode)
	}
	if !m.CapturingInput() {
		t.Fatalf("modePortForward captures input")
	}
	if !strings.Contains(m.remotePortIn.Value(), "5432") {
		t.Fatalf("remote port should default to instance port, got %q", m.remotePortIn.Value())
	}
}

func TestBuildPortForwardCommand(t *testing.T) {
	cmd, err := buildPortForwardCommand("prod", "us-east-1", "i-abc", "host.x", "5432", "15432")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{
		"aws ssm start-session",
		"--profile prod",
		"--region us-east-1",
		"--target i-abc",
		"AWS-StartPortForwardingSessionToRemoteHost",
		`"host":["host.x"]`,
		`"portNumber":["5432"]`,
		`"localPortNumber":["15432"]`,
	}
	for _, w := range want {
		if !strings.Contains(cmd, w) {
			t.Fatalf("cmd missing %q: %s", w, cmd)
		}
	}
}

func TestBuildPortForwardRejectsBadInputs(t *testing.T) {
	cases := []struct {
		name, bastion, host, rp, lp string
	}{
		{"empty bastion", "", "h", "5432", "15432"},
		{"bastion not i-", "abc", "h", "5432", "15432"},
		{"missing host", "i-x", "", "5432", "15432"},
		{"missing remote", "i-x", "h", "", "15432"},
		{"missing local", "i-x", "h", "5432", ""},
	}
	for _, c := range cases {
		if _, err := buildPortForwardCommand("p", "r", c.bastion, c.host, c.rp, c.lp); err == nil {
			t.Fatalf("%s: expected error, got nil", c.name)
		}
	}
}
