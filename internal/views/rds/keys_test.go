package rds

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// EC2 and RDS both present a three-field, single-line port-forward form with
// tab/shift+tab between fields, but EC2 submitted on enter while RDS wanted
// ctrl+s. Both are enter now.
//
// The assertions below deliberately drive the form with an empty endpoint so
// submission fails validation instead of reaching the clipboard: the point is
// which key runs the handler, and a validation string is deterministic where a
// clipboard write is not.

func portForwardModel() Model {
	m := testModel()
	m.mode = modePortForward
	return m
}

func TestPortForwardSubmitsOnEnter(t *testing.T) {
	m := portForwardModel()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	if m.status != "RDS endpoint missing" {
		t.Fatalf("enter should run the port-forward builder, got status %q", m.status)
	}
}

func TestPortForwardIgnoresOldCtrlS(t *testing.T) {
	m := portForwardModel()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updated.(Model)

	if m.status != "" {
		t.Fatalf("ctrl+s should no longer submit the form, got status %q", m.status)
	}
}

func TestPortForwardEnterBuildsCommand(t *testing.T) {
	m := portForwardModel()
	m.target = DBInstance{ID: "db-1", Endpoint: "db.example.rds.amazonaws.com", Port: 5432}
	m.bastionInput.SetValue("i-0123456789abcdef0")
	m.remotePortIn.SetValue("5432")
	m.localPortIn.SetValue("15432")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	// doYank reports either success or a clipboard failure depending on the
	// environment; both prove enter reached the builder past validation.
	if m.status == "" || m.status == "RDS endpoint missing" {
		t.Fatalf("enter with a complete form should build the command, got status %q", m.status)
	}
}

// Refresh moved from 'r' to ctrl+r.

func TestCtrlRRefreshesInstances(t *testing.T) {
	m := testModel()
	key := "rds:instances:" + m.ctx.Region
	m.ctx.Cache.Set(key, []DBInstance{{ID: "stale"}}, time.Minute)

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
