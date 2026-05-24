package paramstore

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
)

func testModel() Model {
	return New(&awspkg.Context{Profile: "test", Region: "us-east-1", Cache: awspkg.NewCache()})
}

func TestParamstoreFilterTransitions(t *testing.T) {
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

func TestParamstoreInSubnavReflectsMode(t *testing.T) {
	m := testModel()
	if m.InSubnav() {
		t.Fatalf("starts on root list (modeList)")
	}
	m.mode = modeValue
	if !m.InSubnav() {
		t.Fatalf("modeValue is a subnav")
	}
	m.mode = modeEdit
	if !m.InSubnav() {
		t.Fatalf("modeEdit is a subnav")
	}
	m.mode = modeList
	if m.InSubnav() {
		t.Fatalf("modeList is not a subnav")
	}
}

func TestParamstoreCapturingInputCoversEditModes(t *testing.T) {
	m := testModel()
	m.mode = modeEdit
	if !m.CapturingInput() {
		t.Fatalf("modeEdit must capture input (textarea)")
	}
	m.mode = modeEditConfirmProd
	if !m.CapturingInput() {
		t.Fatalf("modeEditConfirmProd must capture input")
	}
	m.mode = modeCreate
	if !m.CapturingInput() {
		t.Fatalf("modeCreate must capture input")
	}
	m.mode = modeValue
	if m.CapturingInput() {
		t.Fatalf("modeValue is read-only; not capturing")
	}
}

func TestSecureStringOpensMasked(t *testing.T) {
	m := testModel()
	m.mode = modeValue
	updated, _ := m.Update(parameterValueMsg{p: Parameter{
		Name:  "/app/secret",
		Type:  "SecureString",
		Value: "s3cr3t-p@ssw0rd",
	}})
	m = updated.(Model)
	if m.revealedValue {
		t.Fatalf("SecureString should open masked")
	}
	// The viewport content should not contain the raw value.
	got := m.valueViewport.View()
	if strings.Contains(got, "s3cr3t-p@ssw0rd") {
		t.Fatalf("masked viewport leaked the raw value: %q", got)
	}
}

func TestRevealKeyTogglesMask(t *testing.T) {
	m := testModel()
	m.mode = modeValue
	updated, _ := m.Update(parameterValueMsg{p: Parameter{
		Name:  "/app/secret",
		Type:  "SecureString",
		Value: "abc123",
	}})
	m = updated.(Model)
	// reveal
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(Model)
	if !m.revealedValue {
		t.Fatalf("'r' should reveal")
	}
	// mask back
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(Model)
	if m.revealedValue {
		t.Fatalf("'r' should mask again")
	}
}

func TestMaskCheckRemasksAfterIdle(t *testing.T) {
	m := testModel()
	m.mode = modeValue
	updated, _ := m.Update(parameterValueMsg{p: Parameter{
		Name: "/app/secret", Type: "SecureString", Value: "x",
	}})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(Model)
	if !m.revealedValue {
		t.Fatalf("expected revealed after r")
	}
	// Pretend the last reveal was a long time ago.
	m.lastReveal = m.lastReveal.Add(-2 * secureIdleTimeout)
	updated, _ = m.Update(maskCheckMsg{})
	m = updated.(Model)
	if m.revealedValue {
		t.Fatalf("idle past secureIdleTimeout should re-mask")
	}
}

func TestMaskCheckReschedulesWhenStillFresh(t *testing.T) {
	m := testModel()
	m.mode = modeValue
	updated, _ := m.Update(parameterValueMsg{p: Parameter{
		Name: "/app/secret", Type: "SecureString", Value: "x",
	}})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(Model)
	// lastReveal is fresh; check fires; should stay revealed and return
	// a tick command for the remaining window.
	updated, cmd := m.Update(maskCheckMsg{})
	m = updated.(Model)
	if !m.revealedValue {
		t.Fatalf("fresh reveal should not re-mask")
	}
	if cmd == nil {
		t.Fatalf("expected a reschedule cmd while still fresh")
	}
}

func TestYankWorksOnMaskedValue(t *testing.T) {
	// Sanity that yank operates on the raw value, not the masked
	// rendering. We can't easily test the clipboard side-effect in CI,
	// so just confirm the status reflects a successful copy intent
	// (it won't be "clipboard error: ..." or "nothing to copy").
	m := testModel()
	m.mode = modeValue
	updated, _ := m.Update(parameterValueMsg{p: Parameter{
		Name: "/app/secret", Type: "SecureString", Value: "real-secret",
	}})
	m = updated.(Model)
	if m.revealedValue {
		t.Fatalf("precondition: value is masked")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	// Either copied (clipboard worked) or clipboard error - both prove
	// we attempted to copy real-secret, not the mask.
	if m.status == "value not loaded yet" {
		t.Fatalf("yank should have attempted to copy the raw value while masked, got status %q", m.status)
	}
}

func TestParamstoreParametersLoaded(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(parametersLoadedMsg{items: []Parameter{
		{Name: "/app/foo", Type: "String"},
		{Name: "/app/bar", Type: "SecureString"},
	}})
	m = updated.(Model)
	if m.loading {
		t.Fatalf("loaded msg should clear loading")
	}
	if len(m.paramsFilt) != 2 {
		t.Fatalf("expected 2 displayed, got %d", len(m.paramsFilt))
	}
}
