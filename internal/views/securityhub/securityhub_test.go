package securityhub

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
)

func testModel() Model {
	return New(&awspkg.Context{Profile: "test", Region: "us-east-1", Cache: awspkg.NewCache()})
}

func TestSecurityHubFilterTransitions(t *testing.T) {
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

func TestSecurityHubSeverityTogglesFilter(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(findingsLoadedMsg{scope: "All", items: []Finding{
		{ID: "f1", Severity: "CRITICAL"},
		{ID: "f2", Severity: "HIGH"},
		{ID: "f3", Severity: "INFORMATIONAL"},
	}})
	m = updated.(Model)
	m.mode = modeFindings
	// all severities on by default
	if len(m.findingsFilt) != 3 {
		t.Fatalf("expected 3 findings with default filter, got %d", len(m.findingsFilt))
	}
	// press '1' to toggle Critical off
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	m = updated.(Model)
	if len(m.findingsFilt) != 2 {
		t.Fatalf("expected 2 findings after toggling Critical off, got %d", len(m.findingsFilt))
	}
}

func TestSecurityHubInsightOwnerDetection(t *testing.T) {
	awsArn := "arn:aws:securityhub:::insight/securityhub/foo/uuid"
	youArn := "arn:aws:securityhub:us-east-1:123:insight/custom/uuid"
	if insightOwner(awsArn) != "AWS" {
		t.Fatalf("expected AWS for %s", awsArn)
	}
	if insightOwner(youArn) != "You" {
		t.Fatalf("expected You for %s", youArn)
	}
}
