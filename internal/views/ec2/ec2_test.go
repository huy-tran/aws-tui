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
	if len(m.displayed) != 2 {
		t.Fatalf("expected displayed populated, got %d", len(m.displayed))
	}
}

func TestEC2FilterNarrows(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(loadedMsg{instances: []Instance{
		{ID: "i-1", Name: "web-prod"},
		{ID: "i-2", Name: "db-staging"},
		{ID: "i-3", Name: "web-staging"},
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
