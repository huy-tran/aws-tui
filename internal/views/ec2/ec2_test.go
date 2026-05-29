package ec2

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
)

func testModel() Model {
	return New(&awspkg.Context{Profile: "test", Region: "us-east-1", Cache: awspkg.NewCache()})
}

func TestEC2FilterTransitions(t *testing.T) {
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
	if m.filter.Value() != "" {
		t.Fatalf("esc should clear filter, got %q", m.filter.Value())
	}
}

func TestEC2LoadedPopulatesInstances(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(loadedMsg{instances: []Instance{
		{ID: "i-1", Name: "web", State: "running"},
		{ID: "i-2", Name: "db", State: "stopped"},
	}})
	m = updated.(Model)
	if m.loading {
		t.Fatalf("loadedMsg should clear loading")
	}
	if len(m.instances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(m.instances))
	}
	// Default state filter is "running", so only the running instance shows.
	if len(m.displayed) != 1 {
		t.Fatalf("expected 1 displayed (running default), got %d", len(m.displayed))
	}
	if m.displayed[0].ID != "i-1" {
		t.Fatalf("expected running instance displayed, got %q", m.displayed[0].ID)
	}
}

func TestEC2FilterNarrows(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(loadedMsg{instances: []Instance{
		{ID: "i-1", Name: "web-prod", State: "running"},
		{ID: "i-2", Name: "db-staging", State: "running"},
		{ID: "i-3", Name: "web-staging", State: "running"},
	}})
	m = updated.(Model)
	// open filter and type "web"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = updated.(Model)
	for _, r := range "web" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	if len(m.displayed) != 2 {
		t.Fatalf("expected 2 web-* instances, got %d", len(m.displayed))
	}
}

func TestEC2StateFilterDefaultsToRunning(t *testing.T) {
	m := testModel()
	if got := m.stateFilter(); got != "running" {
		t.Fatalf("expected default state filter 'running', got %q", got)
	}
}

func TestEC2StateFilterPicker(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(loadedMsg{instances: []Instance{
		{ID: "i-1", Name: "web", State: "running"},
		{ID: "i-2", Name: "db", State: "stopped"},
		{ID: "i-3", Name: "old", State: "terminated"},
	}})
	m = updated.(Model)

	// running (default) -> only the running instance.
	if len(m.displayed) != 1 || m.displayed[0].ID != "i-1" {
		t.Fatalf("running filter: expected [i-1], got %+v", m.displayed)
	}

	// press 'f' then a key; returns the resulting model.
	pick := func(key string) {
		u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
		m = u.(Model)
		if !m.statePicking {
			t.Fatalf("f should open the state picker")
		}
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m = u.(Model)
		if m.statePicking {
			t.Fatalf("picking a state should close the ribbon")
		}
	}

	// [5] stopped -> i-2.
	pick("5")
	if m.stateFilter() != "stopped" || len(m.displayed) != 1 || m.displayed[0].ID != "i-2" {
		t.Fatalf("digit 5 should select stopped/[i-2], got %q %+v", m.stateFilter(), m.displayed)
	}

	// first-letter 't' -> terminated -> i-3.
	pick("t")
	if m.stateFilter() != "terminated" || len(m.displayed) != 1 || m.displayed[0].ID != "i-3" {
		t.Fatalf("letter t should select terminated/[i-3], got %q %+v", m.stateFilter(), m.displayed)
	}

	// [1] all -> everything.
	pick("1")
	if m.stateFilter() != stateFilterAll || len(m.displayed) != 3 {
		t.Fatalf("digit 1 should select all (3 rows), got %q %d", m.stateFilter(), len(m.displayed))
	}
}

func TestEC2StatePickerEscCancels(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(loadedMsg{instances: []Instance{
		{ID: "i-1", State: "running"},
	}})
	m = updated.(Model)

	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = u.(Model)
	if !m.CapturingInput() {
		t.Fatalf("picker should capture input")
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = u.(Model)
	if m.statePicking {
		t.Fatalf("esc should close the picker")
	}
	if m.stateFilter() != "running" {
		t.Fatalf("esc should leave the filter unchanged, got %q", m.stateFilter())
	}
}
