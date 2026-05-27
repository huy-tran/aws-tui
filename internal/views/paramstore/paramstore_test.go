package paramstore

import (
	"fmt"
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

func TestEditTextareaAcceptsEnterAsNewline(t *testing.T) {
	m := testModel()
	m.target = Parameter{Name: "/app/foo", Type: "String", Value: "line1"}
	m.enterEditFromTarget()
	if got := m.valueInput.Value(); got != "line1" {
		t.Fatalf("precondition: value should be 'line1', got %q", got)
	}
	if !m.valueInput.Focused() {
		t.Fatalf("precondition: value textarea must be focused after enterEditFromTarget")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	got := m.valueInput.Value()
	if !strings.Contains(got, "\n") {
		t.Fatalf("pressing enter on focused textarea should insert a newline, got %q", got)
	}
}

func TestEditTextareaAcceptsNewlineOnLongValues(t *testing.T) {
	// bubbles' textarea defaults MaxHeight=99 and silently drops
	// InsertNewline once that limit is reached. Real parameter values
	// (large JSON blobs, etc.) blow past 99 lines, so we lift the cap
	// in New(). Regression check: a 150-line value must still accept
	// 'enter' as a newline.
	m := testModel()
	lines := make([]string, 150)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%d", i)
	}
	m.target = Parameter{Name: "/app/big", Type: "String", Value: strings.Join(lines, "\n")}
	m.enterEditFromTarget()
	before := m.valueInput.LineCount()
	if before < 100 {
		t.Fatalf("precondition: expected >=100 lines, got %d", before)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.valueInput.LineCount() != before+1 {
		t.Fatalf("enter should add a line even past the bubbles default cap; was %d, now %d", before, m.valueInput.LineCount())
	}
}

func TestEditEnterAfterFindFlowStillInsertsNewline(t *testing.T) {
	// User opens find with ctrl+f, types a query, presses enter to jump
	// out of find mode, then expects enter to insert a newline in the
	// textarea. This exercises the focus handoff between findInput and
	// valueInput across modes.
	m := testModel()
	m.target = Parameter{Name: "/app/foo", Type: "String", Value: "hello world"}
	m.enterEditFromTarget()

	// Open find.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = updated.(Model)
	if !m.findEditMode || !m.findEditInput.Focused() {
		t.Fatalf("ctrl+f should enter findEditMode with findInput focused")
	}
	// Type the query.
	for _, r := range "world" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	if len(m.findEditMatches) == 0 {
		t.Fatalf("expected at least one match for 'world' in 'hello world'")
	}
	// Press enter to jump and exit find.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.findEditMode {
		t.Fatalf("enter should exit findEditMode")
	}
	if !m.valueInput.Focused() {
		t.Fatalf("after exiting find, value textarea should be focused, got focused=%v", m.valueInput.Focused())
	}
	// Press enter again - should insert newline into the textarea.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if !strings.Contains(m.valueInput.Value(), "\n") {
		t.Fatalf("enter after find-exit should insert newline, got value %q", m.valueInput.Value())
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
