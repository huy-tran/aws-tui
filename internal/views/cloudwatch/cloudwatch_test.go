package cloudwatch

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
)

func testModel() Model {
	return New(&awspkg.Context{Profile: "test", Region: "us-east-1", Cache: awspkg.NewCache()})
}

func TestCloudWatchFilterTransitions(t *testing.T) {
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

func TestCloudWatchInSubnavReflectsMode(t *testing.T) {
	m := testModel()
	if m.InSubnav() {
		t.Fatalf("modeGroups is root")
	}
	m.mode = modeStreams
	if !m.InSubnav() {
		t.Fatalf("modeStreams is a subnav")
	}
	m.mode = modeSearch
	if !m.InSubnav() {
		t.Fatalf("modeSearch is a subnav")
	}
	if !m.CapturingInput() {
		t.Fatalf("modeSearch should capture (pattern input)")
	}
}

func TestCloudWatchGroupsLoaded(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(groupsLoadedMsg{items: []LogGroup{
		{Name: "/aws/lambda/x"},
		{Name: "/aws/lambda/y"},
	}})
	m = updated.(Model)
	if m.groupsLoading {
		t.Fatalf("groupsLoaded should clear loading")
	}
	if len(m.groupsFilt) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(m.groupsFilt))
	}
}
