package cloudfront

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
)

func testModel() Model {
	return New(&awspkg.Context{Profile: "test", Region: "us-east-1", Cache: awspkg.NewCache()})
}

func TestCloudFrontFilterTransitions(t *testing.T) {
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

func TestCloudFrontInSubnavReflectsMode(t *testing.T) {
	m := testModel()
	if m.InSubnav() {
		t.Fatalf("starts on modeList; not in subnav")
	}
	m.mode = modeInvalidations
	if !m.InSubnav() {
		t.Fatalf("modeInvalidations is a subnav")
	}
	m.mode = modeInvalidatePaths
	if !m.InSubnav() {
		t.Fatalf("modeInvalidatePaths is a subnav")
	}
	if !m.CapturingInput() {
		t.Fatalf("modeInvalidatePaths should capture (textarea)")
	}
}

func TestCloudFrontLoaded(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(distributionsLoadedMsg{items: []Distribution{
		{ID: "E1ABC", DomainName: "d.example.com", Status: "Deployed"},
	}})
	m = updated.(Model)
	if m.loading {
		t.Fatalf("loaded msg should clear loading")
	}
	if len(m.distrosFilt) != 1 {
		t.Fatalf("expected 1 distro, got %d", len(m.distrosFilt))
	}
}
