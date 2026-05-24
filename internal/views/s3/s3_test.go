package s3

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/state"
)

func testModel() Model {
	return New(
		&awspkg.Context{Profile: "test", Region: "us-east-1", Cache: awspkg.NewCache()},
		&state.Store{},
	)
}

func TestS3FilterTransitions(t *testing.T) {
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

func TestS3YankPendingState(t *testing.T) {
	m := testModel()
	// Region pre-populated so loadRegionsCmd returns nil (no AWS call attempt).
	updated, _ := m.Update(bucketsLoadedMsg{items: []Bucket{{Name: "test-bucket", Region: "us-east-1"}}})
	m = updated.(Model)
	if m.CapturingInput() {
		t.Fatalf("not capturing in steady list state")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	if !m.CapturingInput() {
		t.Fatalf("'y' should put view into yank-pending (capturing)")
	}
	// next key clears yankPending
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(Model)
	if m.CapturingInput() {
		t.Fatalf("yankPending should clear after a follow-up key")
	}
}
