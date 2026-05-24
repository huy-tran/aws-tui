package beanstalk

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
)

func testModel() Model {
	return New(&awspkg.Context{Profile: "test", Region: "us-east-1", Cache: awspkg.NewCache()})
}

func TestBeanstalkFilterTransitions(t *testing.T) {
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

func TestBeanstalkInSubnavReflectsMode(t *testing.T) {
	m := testModel()
	if m.InSubnav() {
		t.Fatalf("starts on modeList; not in subnav")
	}
	m.mode = modeDetails
	if !m.InSubnav() {
		t.Fatalf("modeDetails is a subnav")
	}
	m.mode = modeDeployConfirm
	if !m.InSubnav() {
		t.Fatalf("modeDeployConfirm is a subnav")
	}
	if !m.CapturingInput() {
		t.Fatalf("modeDeployConfirm should capture input for name-typed confirm")
	}
}

func TestBeanstalkLoadedPopulatesEnvs(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(envsLoadedMsg{items: []Environment{
		{AppName: "app", EnvName: "prod", Health: "Green", Status: "Ready"},
	}})
	m = updated.(Model)
	if m.loading {
		t.Fatalf("envsLoadedMsg should clear loading")
	}
	if len(m.envsFilt) != 1 {
		t.Fatalf("expected 1 env displayed, got %d", len(m.envsFilt))
	}
}
