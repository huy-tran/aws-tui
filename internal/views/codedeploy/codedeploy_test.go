package codedeploy

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
)

func testModel() Model {
	return New(&awspkg.Context{Profile: "test", Region: "us-east-1", Cache: awspkg.NewCache()})
}

func key(m Model, s string) Model {
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return updated.(Model)
}

func TestStartsAtAppsNotCapturing(t *testing.T) {
	m := testModel()
	if m.level != levelApps {
		t.Fatalf("expected to start at apps level")
	}
	if m.CapturingInput() {
		t.Fatalf("should not capture input at rest")
	}
	if m.InSubnav() {
		t.Fatalf("apps level should not be a subnav")
	}
}

func TestAppsLoadedPopulates(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(appsLoadedMsg{items: []App{
		{Name: "web", Platform: "Server"},
		{Name: "api", Platform: "Lambda"},
	}})
	m = updated.(Model)
	if m.loading {
		t.Fatalf("appsLoadedMsg should clear loading")
	}
	if len(m.apps) != 2 || len(m.appsF) != 2 {
		t.Fatalf("expected 2 apps displayed, got %d/%d", len(m.apps), len(m.appsF))
	}
}

func TestFilterNarrowsApps(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(appsLoadedMsg{items: []App{
		{Name: "web-prod"},
		{Name: "api-prod"},
		{Name: "web-staging"},
	}})
	m = updated.(Model)
	m = key(m, "/")
	if !m.CapturingInput() {
		t.Fatalf("/ should open filter")
	}
	for _, r := range "web" {
		m = key(m, string(r))
	}
	if len(m.appsF) != 2 {
		t.Fatalf("expected 2 web-* apps, got %d", len(m.appsF))
	}
	// esc clears and exits the filter.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.CapturingInput() || m.filter.Value() != "" {
		t.Fatalf("esc should clear+exit filter")
	}
	if len(m.appsF) != 3 {
		t.Fatalf("after clearing filter expected 3 apps, got %d", len(m.appsF))
	}
}

// drillToDeployments walks apps -> groups -> deployments and loads the given
// deployment list, returning a model sitting at the deployments level.
func drillToDeployments(t *testing.T, deps []Deployment) Model {
	t.Helper()
	m := testModel()
	updated, _ := m.Update(appsLoadedMsg{items: []App{{Name: "web"}}})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(groupsLoadedMsg{app: "web", items: []Group{{Name: "prod"}}})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(deploymentsLoadedMsg{app: "web", group: "prod", items: deps})
	m = updated.(Model)
	if m.level != levelDeployments {
		t.Fatalf("expected deployments level, got %d", m.level)
	}
	return m
}

func TestDeploymentsDefaultSortCreatedDesc(t *testing.T) {
	// Loaded in arbitrary order; the table should present them newest-first.
	m := drillToDeployments(t, []Deployment{
		{ID: "d-1", Created: "2026-05-01 10:00:00"},
		{ID: "d-2", Created: "2026-05-03 10:00:00"},
		{ID: "d-3", Created: "2026-05-02 10:00:00"},
	})
	rows := m.deployTable.Rows()
	got := []string{rows[0][0], rows[1][0], rows[2][0]}
	want := []string{"d-2", "d-3", "d-1"} // newest -> oldest
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("default order = %v, want created-descending %v", got, want)
		}
	}
}

func TestDeploymentsSortByCreated(t *testing.T) {
	m := drillToDeployments(t, []Deployment{
		{ID: "d-1", Created: "2026-05-01 10:00:00"},
		{ID: "d-2", Created: "2026-05-03 10:00:00"},
		{ID: "d-3", Created: "2026-05-02 10:00:00"},
	})

	// 's' opens the datatable sort ribbon; the tab must report capturing so
	// the dashboard does not steal the next key.
	m = key(m, "s")
	if !m.CapturingInput() {
		t.Fatalf("s should open the sort ribbon and capture input")
	}
	// Column 4 is "Created"; picking it sorts ascending and closes the ribbon.
	m = key(m, "4")
	if m.CapturingInput() {
		t.Fatalf("picking a sort column should close the ribbon")
	}

	rows := m.deployTable.Rows()
	got := []string{rows[0][0], rows[1][0], rows[2][0]}
	want := []string{"d-1", "d-3", "d-2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("created-ascending order = %v, want %v", got, want)
		}
	}
}

func TestSortRibbonKeysDontLeak(t *testing.T) {
	// While the ribbon is up, 'esc' must cancel the sort, not ascend a level.
	m := drillToDeployments(t, []Deployment{{ID: "d-1"}})
	m = key(m, "s")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.level != levelDeployments {
		t.Fatalf("esc during sort-pick should cancel the ribbon, not ascend (level=%d)", m.level)
	}
	if m.CapturingInput() {
		t.Fatalf("esc should close the sort ribbon")
	}
}

func TestDrillDownAndBack(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(appsLoadedMsg{items: []App{{Name: "web"}}})
	m = updated.(Model)

	// enter -> groups level, loading the selected app.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.level != levelGroups {
		t.Fatalf("enter on app should descend to groups, got level %d", m.level)
	}
	if m.selApp != "web" {
		t.Fatalf("descend should record selApp, got %q", m.selApp)
	}
	if !m.InSubnav() {
		t.Fatalf("groups level should report InSubnav")
	}

	updated, _ = m.Update(groupsLoadedMsg{app: "web", items: []Group{{Name: "prod"}}})
	m = updated.(Model)
	if len(m.groupsF) != 1 {
		t.Fatalf("expected 1 group, got %d", len(m.groupsF))
	}

	// enter -> deployments.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.level != levelDeployments || m.selGrp != "prod" {
		t.Fatalf("expected deployments level for group prod, got level %d grp %q", m.level, m.selGrp)
	}

	updated, _ = m.Update(deploymentsLoadedMsg{app: "web", group: "prod", items: []Deployment{
		{ID: "d-123", Status: "Succeeded"},
	}})
	m = updated.(Model)

	// enter -> detail (no further load).
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.level != levelDetail || m.selDep.ID != "d-123" {
		t.Fatalf("expected detail of d-123, got level %d id %q", m.level, m.selDep.ID)
	}

	// esc walks back up one level at a time.
	for _, want := range []level{levelDeployments, levelGroups, levelApps} {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = updated.(Model)
		if m.level != want {
			t.Fatalf("esc should ascend to level %d, got %d", want, m.level)
		}
	}
	if m.InSubnav() {
		t.Fatalf("back at apps level InSubnav should be false")
	}
}
