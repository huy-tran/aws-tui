// Package codedeploy is the read-only CodeDeploy browser tab. It drills
// through the service's natural hierarchy - Applications -> Deployment Groups
// -> Deployments -> a single deployment's detail - using one in-tab "level"
// rather than pushing separate views, mirroring the Beanstalk tab. Each level
// loads lazily and is cached; enter descends, esc ascends.
package codedeploy

import (
	"context"
	"encoding/gob"
	"fmt"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	cd "github.com/aws/aws-sdk-go-v2/service/codedeploy"
	cdtypes "github.com/aws/aws-sdk-go-v2/service/codedeploy/types"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/loader"
	"github.com/huy-tran/aws-tui/internal/ui/statusbar"
)

const (
	appsCacheTTL        = 5 * time.Minute
	groupsCacheTTL      = 2 * time.Minute
	deploymentsCacheTTL = 30 * time.Second
	// batchLimit is the max names/ids per BatchGet* call.
	batchLimit = 100
	// deployCreatedCol is the index of the "Created" column in the
	// deployments table; the list defaults to sorting by it, descending.
	deployCreatedCol = 3
)

func init() {
	gob.Register([]App(nil))
	gob.Register([]Group(nil))
	gob.Register([]Deployment(nil))
}

// App is one CodeDeploy application.
type App struct {
	Name     string
	Platform string
	Created  string
}

// Group is one deployment group, summarised with its most recent attempt.
type Group struct {
	Name        string
	Platform    string
	ConfigName  string
	LastStatus  string
	LastDeploy  string
	ServiceRole string
}

// Deployment is one deployment, carrying enough detail that the detail panel
// needs no further API call.
type Deployment struct {
	ID          string
	Status      string
	Creator     string
	Created     string
	Completed   string
	App         string
	Group       string
	ConfigName  string
	Description string
	Revision    string
	ErrCode     string
	ErrMessage  string
	Succeeded   int64
	Failed      int64
	InProgress  int64
	Pending     int64
	Skipped     int64
	Ready       int64
}

type (
	appsLoadedMsg        struct{ items []App }
	groupsLoadedMsg      struct {
		app   string
		items []Group
	}
	deploymentsLoadedMsg struct {
		app, group string
		items      []Deployment
	}
	errMsg struct{ err error }
)

type level int

const (
	levelApps level = iota
	levelGroups
	levelDeployments
	levelDetail
)

type Model struct {
	ctx   *awspkg.Context
	level level

	apps   []App
	appsF  []App
	groups []Group
	groupsF []Group
	deps   []Deployment
	depsF  []Deployment

	appTable    datatable.Model
	groupTable  datatable.Model
	deployTable datatable.Model

	filter     textinput.Model
	filterMode bool

	selApp string // selected application name (set when descending)
	selGrp string // selected deployment group name
	selDep Deployment

	loading    bool
	loader     loader.Model
	err        error
	status     string
	lastLoaded time.Time
	width      int
	height     int
}

func New(ctx *awspkg.Context) Model {
	appCols := []datatable.Column{
		{Title: "Application", Flex: true},
		{Title: "Platform"},
		{Title: "Created", SortAs: datatable.SortTime},
	}
	groupCols := []datatable.Column{
		{Title: "Deployment Group", Flex: true},
		{Title: "Platform"},
		{Title: "Last Status"},
		{Title: "Last Deployment", SortAs: datatable.SortTime},
	}
	deployCols := []datatable.Column{
		{Title: "Deployment ID", Flex: true},
		{Title: "Status"},
		{Title: "Creator"},
		{Title: "Created", SortAs: datatable.SortTime},
		{Title: "Completed", SortAs: datatable.SortTime},
	}

	ti := textinput.New()
	ti.Placeholder = "filter"
	ti.CharLimit = 64
	ti.Prompt = "/ "

	deployTable := datatable.New(deployCols)
	// Default the deployments list to newest-first (Created descending),
	// matching the order CodeDeploy returns and what operators expect.
	deployTable.SetSort(deployCreatedCol, -1)

	return Model{
		ctx:         ctx,
		appTable:    datatable.New(appCols),
		groupTable:  datatable.New(groupCols),
		deployTable: deployTable,
		filter:      ti,
		loader:      loader.New(),
		loading:     true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.loadAppsCmd(false))
}

// ---------- loading ----------

func (m Model) loadAppsCmd(force bool) tea.Cmd {
	cacheKey := "codedeploy:apps:" + m.ctx.Region
	ctx := m.ctx
	return func() tea.Msg {
		if !force {
			if cached, ok := ctx.Cache.Get(cacheKey); ok {
				if apps, ok := cached.([]App); ok {
					return appsLoadedMsg{items: apps}
				}
			}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := ctx.CodeDeploy()
		var names []string
		var token *string
		for {
			out, err := client.ListApplications(context.Background(), &cd.ListApplicationsInput{NextToken: token})
			if err != nil {
				return errMsg{err: err}
			}
			names = append(names, out.Applications...)
			if out.NextToken == nil {
				break
			}
			token = out.NextToken
		}
		var apps []App
		for _, chunk := range chunk(names, batchLimit) {
			out, err := client.BatchGetApplications(context.Background(), &cd.BatchGetApplicationsInput{ApplicationNames: chunk})
			if err != nil {
				return errMsg{err: err}
			}
			for _, info := range out.ApplicationsInfo {
				apps = append(apps, App{
					Name:     awssdk.ToString(info.ApplicationName),
					Platform: string(info.ComputePlatform),
					Created:  fmtTime(info.CreateTime),
				})
			}
		}
		ctx.Cache.Set(cacheKey, apps, appsCacheTTL)
		return appsLoadedMsg{items: apps}
	}
}

func (m Model) loadGroupsCmd(app string, force bool) tea.Cmd {
	cacheKey := "codedeploy:groups:" + m.ctx.Region + ":" + app
	ctx := m.ctx
	return func() tea.Msg {
		if !force {
			if cached, ok := ctx.Cache.Get(cacheKey); ok {
				if groups, ok := cached.([]Group); ok {
					return groupsLoadedMsg{app: app, items: groups}
				}
			}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := ctx.CodeDeploy()
		var names []string
		var token *string
		for {
			out, err := client.ListDeploymentGroups(context.Background(), &cd.ListDeploymentGroupsInput{
				ApplicationName: &app,
				NextToken:       token,
			})
			if err != nil {
				return errMsg{err: err}
			}
			names = append(names, out.DeploymentGroups...)
			if out.NextToken == nil {
				break
			}
			token = out.NextToken
		}
		var groups []Group
		for _, ch := range chunk(names, batchLimit) {
			out, err := client.BatchGetDeploymentGroups(context.Background(), &cd.BatchGetDeploymentGroupsInput{
				ApplicationName:      &app,
				DeploymentGroupNames: ch,
			})
			if err != nil {
				return errMsg{err: err}
			}
			for _, info := range out.DeploymentGroupsInfo {
				g := Group{
					Name:        awssdk.ToString(info.DeploymentGroupName),
					Platform:    string(info.ComputePlatform),
					ConfigName:  awssdk.ToString(info.DeploymentConfigName),
					ServiceRole: awssdk.ToString(info.ServiceRoleArn),
				}
				last := info.LastAttemptedDeployment
				if last == nil {
					last = info.LastSuccessfulDeployment
				}
				if last != nil {
					g.LastStatus = string(last.Status)
					g.LastDeploy = fmtTime(last.CreateTime)
				}
				groups = append(groups, g)
			}
		}
		ctx.Cache.Set(cacheKey, groups, groupsCacheTTL)
		return groupsLoadedMsg{app: app, items: groups}
	}
}

func (m Model) loadDeploymentsCmd(app, group string, force bool) tea.Cmd {
	cacheKey := "codedeploy:deployments:" + m.ctx.Region + ":" + app + ":" + group
	ctx := m.ctx
	return func() tea.Msg {
		if !force {
			if cached, ok := ctx.Cache.Get(cacheKey); ok {
				if deps, ok := cached.([]Deployment); ok {
					return deploymentsLoadedMsg{app: app, group: group, items: deps}
				}
			}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := ctx.CodeDeploy()
		// First page only: ListDeployments returns most-recent-first, which
		// is what we want to show. A TUI doesn't need the full history.
		listed, err := client.ListDeployments(context.Background(), &cd.ListDeploymentsInput{
			ApplicationName:     &app,
			DeploymentGroupName: &group,
		})
		if err != nil {
			return errMsg{err: err}
		}
		var deps []Deployment
		for _, ch := range chunk(listed.Deployments, batchLimit) {
			out, err := client.BatchGetDeployments(context.Background(), &cd.BatchGetDeploymentsInput{DeploymentIds: ch})
			if err != nil {
				return errMsg{err: err}
			}
			for _, info := range out.DeploymentsInfo {
				deps = append(deps, parseDeployment(info))
			}
		}
		ctx.Cache.Set(cacheKey, deps, deploymentsCacheTTL)
		return deploymentsLoadedMsg{app: app, group: group, items: deps}
	}
}

func parseDeployment(info cdtypes.DeploymentInfo) Deployment {
	d := Deployment{
		ID:          awssdk.ToString(info.DeploymentId),
		Status:      string(info.Status),
		Creator:     string(info.Creator),
		Created:     fmtTime(info.CreateTime),
		Completed:   fmtTime(info.CompleteTime),
		App:         awssdk.ToString(info.ApplicationName),
		Group:       awssdk.ToString(info.DeploymentGroupName),
		ConfigName:  awssdk.ToString(info.DeploymentConfigName),
		Description: awssdk.ToString(info.Description),
		Revision:    revisionString(info.Revision),
	}
	if info.ErrorInformation != nil {
		d.ErrCode = string(info.ErrorInformation.Code)
		d.ErrMessage = awssdk.ToString(info.ErrorInformation.Message)
	}
	if o := info.DeploymentOverview; o != nil {
		d.Succeeded = o.Succeeded
		d.Failed = o.Failed
		d.InProgress = o.InProgress
		d.Pending = o.Pending
		d.Skipped = o.Skipped
		d.Ready = o.Ready
	}
	return d
}

// revisionString renders the source revision compactly: s3://bucket/key for
// S3 bundles, github:repo@commit for GitHub, else the bare revision type.
func revisionString(r *cdtypes.RevisionLocation) string {
	if r == nil {
		return ""
	}
	switch r.RevisionType {
	case cdtypes.RevisionLocationTypeS3:
		if r.S3Location != nil {
			return fmt.Sprintf("s3://%s/%s", awssdk.ToString(r.S3Location.Bucket), awssdk.ToString(r.S3Location.Key))
		}
	case cdtypes.RevisionLocationTypeGitHub:
		if r.GitHubLocation != nil {
			return fmt.Sprintf("github:%s@%s", awssdk.ToString(r.GitHubLocation.Repository), awssdk.ToString(r.GitHubLocation.CommitId))
		}
	}
	return string(r.RevisionType)
}

func chunk(s []string, size int) [][]string {
	if len(s) == 0 {
		return nil
	}
	var out [][]string
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}

// fmtTime renders a timestamp as "2006-01-02 15:04:05". The seconds matter:
// the datatable's SortTime comparator parses exactly this layout, so trimming
// them would silently demote date columns to lexicographic sorting.
func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// ---------- update ----------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 4
		if h < 3 {
			h = 3
		}
		for _, t := range []*datatable.Model{&m.appTable, &m.groupTable, &m.deployTable} {
			t.SetHeight(h)
			t.SetWidth(msg.Width)
		}
		return m, nil

	case appsLoadedMsg:
		m.loading = false
		m.apps = msg.items
		m.lastLoaded = time.Now()
		m.applyFilter()
		return m, nil

	case groupsLoadedMsg:
		m.loading = false
		m.groups = msg.items
		m.lastLoaded = time.Now()
		m.applyFilter()
		return m, nil

	case deploymentsLoadedMsg:
		m.loading = false
		m.deps = msg.items
		m.lastLoaded = time.Now()
		m.applyFilter()
		return m, nil

	case errMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case tea.KeyMsg:
		if m.filterMode {
			switch msg.String() {
			case "esc":
				m.filterMode = false
				m.filter.SetValue("")
				m.filter.Blur()
				m.applyFilter()
				return m, nil
			case "enter":
				m.filterMode = false
				m.filter.Blur()
				return m, nil
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.applyFilter()
			return m, cmd
		}

		// When the active list table has its sort-picker ribbon up, every key
		// (including esc/enter/r) belongs to that ribbon. Skip our own
		// bindings and let the generic forwarder below hand the key to it.
		if !m.loading && !m.activeTableCapturing() {
			switch msg.String() {
			case "/":
				if m.level != levelDetail {
					m.filterMode = true
					m.filter.Focus()
					return m, textinput.Blink
				}
			case "esc":
				return m.ascend()
			case "enter":
				return m.descend()
			case "r":
				return m.refresh()
			}
		}
	}

	// Forward navigation keys to the active level's table.
	var cmd tea.Cmd
	if m.loading {
		if _, ok := msg.(spinner.TickMsg); ok {
			m.loader, cmd = m.loader.Update(msg)
		}
		return m, cmd
	}
	switch m.level {
	case levelApps:
		m.appTable, cmd = m.appTable.Update(msg)
	case levelGroups:
		m.groupTable, cmd = m.groupTable.Update(msg)
	case levelDeployments:
		m.deployTable, cmd = m.deployTable.Update(msg)
	}
	return m, cmd
}

// descend drills into the highlighted row, loading the next level.
func (m Model) descend() (tea.Model, tea.Cmd) {
	switch m.level {
	case levelApps:
		app := m.selectedApp()
		if app == nil {
			return m, nil
		}
		m.selApp = app.Name
		m.level = levelGroups
		m.clearFilter()
		m.groups = nil
		m.loading = true
		return m, tea.Batch(m.loader.Tick(), m.loadGroupsCmd(app.Name, false))
	case levelGroups:
		grp := m.selectedGroup()
		if grp == nil {
			return m, nil
		}
		m.selGrp = grp.Name
		m.level = levelDeployments
		m.clearFilter()
		m.deps = nil
		m.loading = true
		return m, tea.Batch(m.loader.Tick(), m.loadDeploymentsCmd(m.selApp, grp.Name, false))
	case levelDeployments:
		dep := m.selectedDeployment()
		if dep == nil {
			return m, nil
		}
		m.selDep = *dep
		m.level = levelDetail
		return m, nil
	}
	return m, nil
}

// ascend backs out one level. At the top level it is a no-op.
func (m Model) ascend() (tea.Model, tea.Cmd) {
	switch m.level {
	case levelGroups:
		m.level = levelApps
		m.clearFilter()
	case levelDeployments:
		m.level = levelGroups
		m.clearFilter()
	case levelDetail:
		m.level = levelDeployments
	}
	return m, nil
}

// refresh re-loads the current level, busting its cache.
func (m Model) refresh() (tea.Model, tea.Cmd) {
	m.err = nil
	switch m.level {
	case levelApps:
		m.ctx.Cache.Invalidate("codedeploy:apps:" + m.ctx.Region)
		m.loading = true
		return m, tea.Batch(m.loader.Tick(), m.loadAppsCmd(true))
	case levelGroups:
		m.ctx.Cache.Invalidate("codedeploy:groups:" + m.ctx.Region + ":" + m.selApp)
		m.loading = true
		return m, tea.Batch(m.loader.Tick(), m.loadGroupsCmd(m.selApp, true))
	case levelDeployments:
		m.ctx.Cache.Invalidate("codedeploy:deployments:" + m.ctx.Region + ":" + m.selApp + ":" + m.selGrp)
		m.loading = true
		return m, tea.Batch(m.loader.Tick(), m.loadDeploymentsCmd(m.selApp, m.selGrp, true))
	}
	return m, nil
}

func (m *Model) clearFilter() {
	m.filterMode = false
	m.filter.SetValue("")
	m.filter.Blur()
	m.applyFilter()
}

// applyFilter narrows the slice for the active list level and pushes the rows
// into that level's table. Detail level has nothing to filter.
func (m *Model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	switch m.level {
	case levelApps:
		m.appsF = filterApps(m.apps, q)
		m.appTable.SetRows(appRows(m.appsF))
	case levelGroups:
		m.groupsF = filterGroups(m.groups, q)
		m.groupTable.SetRows(groupRows(m.groupsF))
	case levelDeployments:
		m.depsF = filterDeployments(m.deps, q)
		m.deployTable.SetRows(deployRows(m.depsF))
	}
}

func filterApps(in []App, q string) []App {
	if q == "" {
		return in
	}
	out := make([]App, 0, len(in))
	for _, a := range in {
		if strings.Contains(strings.ToLower(a.Name), q) || strings.Contains(strings.ToLower(a.Platform), q) {
			out = append(out, a)
		}
	}
	return out
}

func filterGroups(in []Group, q string) []Group {
	if q == "" {
		return in
	}
	out := make([]Group, 0, len(in))
	for _, g := range in {
		if strings.Contains(strings.ToLower(g.Name), q) ||
			strings.Contains(strings.ToLower(g.LastStatus), q) ||
			strings.Contains(strings.ToLower(g.ConfigName), q) {
			out = append(out, g)
		}
	}
	return out
}

func filterDeployments(in []Deployment, q string) []Deployment {
	if q == "" {
		return in
	}
	out := make([]Deployment, 0, len(in))
	for _, d := range in {
		if strings.Contains(strings.ToLower(d.ID), q) ||
			strings.Contains(strings.ToLower(d.Status), q) ||
			strings.Contains(strings.ToLower(d.Creator), q) {
			out = append(out, d)
		}
	}
	return out
}

// ---------- selection ----------

func (m Model) selectedApp() *App {
	if m.loading || len(m.appsF) == 0 {
		return nil
	}
	i := m.appTable.Cursor()
	if i < 0 || i >= len(m.appsF) {
		return nil
	}
	return &m.appsF[i]
}

func (m Model) selectedGroup() *Group {
	if m.loading || len(m.groupsF) == 0 {
		return nil
	}
	i := m.groupTable.Cursor()
	if i < 0 || i >= len(m.groupsF) {
		return nil
	}
	return &m.groupsF[i]
}

func (m Model) selectedDeployment() *Deployment {
	if m.loading || len(m.depsF) == 0 {
		return nil
	}
	i := m.deployTable.Cursor()
	if i < 0 || i >= len(m.depsF) {
		return nil
	}
	return &m.depsF[i]
}

// ---------- dashboard interface ----------

// activeTableCapturing reports whether the current list level's datatable is
// mid-interaction (its sort-picker ribbon is up).
func (m Model) activeTableCapturing() bool {
	switch m.level {
	case levelApps:
		return m.appTable.CapturingInput()
	case levelGroups:
		return m.groupTable.CapturingInput()
	case levelDeployments:
		return m.deployTable.CapturingInput()
	}
	return false
}

func (m Model) CapturingInput() bool {
	// filterMode captures text; a table's sort ribbon captures the next key.
	// Either way the dashboard must not steal tab/bookmark keys.
	return m.filterMode || m.activeTableCapturing()
}

func (m Model) InSubnav() bool { return m.level != levelApps }

func (m Model) BookmarkCurrent() (string, string, bool) {
	if m.level != levelApps {
		return "", "", false
	}
	app := m.selectedApp()
	if app == nil {
		return "", "", false
	}
	return app.Name, app.Name, true
}

func (m Model) SetFilterQuery(q string) tea.Model {
	m.level = levelApps
	m.filter.SetValue(q)
	m.applyFilter()
	return m
}

func (m Model) StatusFooter() statusbar.Snapshot {
	items := 0
	switch m.level {
	case levelApps:
		items = len(m.appsF)
	case levelGroups:
		items = len(m.groupsF)
	case levelDeployments:
		items = len(m.depsF)
	case levelDetail:
		items = 1
	}
	return statusbar.Snapshot{Items: items, LastLoaded: m.lastLoaded, Message: m.status}
}

func (m Model) HelpItems() []help.Section {
	if m.filterMode {
		return []help.Section{{
			Title: "CodeDeploy · filter",
			Items: []help.Item{
				{Keys: "type", Desc: "narrow the current list"},
				{Keys: "enter", Desc: "apply and exit filter"},
				{Keys: "esc", Desc: "clear and exit filter"},
			},
		}}
	}
	switch m.level {
	case levelApps:
		return []help.Section{{
			Title: "CodeDeploy · applications",
			Items: []help.Item{
				{Keys: "enter", Desc: "open deployment groups"},
				{Keys: "/", Desc: "filter"},
				{Keys: "s", Desc: "sort by column (pick name / platform / created)"},
				{Keys: "r", Desc: "refresh (invalidates cache)"},
				{Keys: "↑/↓ j/k", Desc: "move cursor"},
			},
		}}
	case levelGroups:
		return []help.Section{{
			Title: "CodeDeploy · deployment groups",
			Items: []help.Item{
				{Keys: "enter", Desc: "open deployments"},
				{Keys: "esc", Desc: "back to applications"},
				{Keys: "/", Desc: "filter"},
				{Keys: "s", Desc: "sort by column (incl. last deployment time)"},
				{Keys: "r", Desc: "refresh"},
			},
		}}
	case levelDeployments:
		return []help.Section{{
			Title: "CodeDeploy · deployments",
			Items: []help.Item{
				{Keys: "enter", Desc: "deployment detail"},
				{Keys: "esc", Desc: "back to groups"},
				{Keys: "/", Desc: "filter"},
				{Keys: "s", Desc: "sort by column, e.g. created / completed (toggle asc/desc)"},
				{Keys: "r", Desc: "refresh"},
			},
		}}
	default:
		return []help.Section{{
			Title: "CodeDeploy · deployment detail",
			Items: []help.Item{
				{Keys: "esc", Desc: "back to deployments"},
			},
		}}
	}
}

// ---------- view ----------

func (m Model) View() string {
	if m.err != nil {
		hint := "Press r to retry."
		if _, ok := m.err.(*awspkg.SSOExpiredError); ok {
			hint = "Run `aws sso login --profile " + m.ctx.Profile + "` in another shell, then press r."
		}
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\n%s", m.err, hint))
	}
	if m.loading {
		return m.loader.Render("loading " + m.levelNoun() + "...")
	}

	header := headerStyle.Render(m.breadcrumb())

	if m.level == levelDetail {
		return lipgloss.JoinVertical(lipgloss.Left, header, m.renderDetail())
	}

	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}

	var body, helpLine string
	switch m.level {
	case levelApps:
		body = m.appTable.View()
		if len(m.appsF) == 0 {
			body = m.emptyBody("applications")
		}
		helpLine = mutedStyle.Render("enter: groups · /: filter · s: sort · r: refresh")
	case levelGroups:
		body = m.groupTable.View()
		if len(m.groupsF) == 0 {
			body = m.emptyBody("deployment groups")
		}
		helpLine = mutedStyle.Render("enter: deployments · esc: back · /: filter · s: sort · r: refresh")
	case levelDeployments:
		body = m.deployTable.View()
		if len(m.depsF) == 0 {
			body = m.emptyBody("deployments")
		}
		helpLine = mutedStyle.Render("enter: detail · esc: back · /: filter · s: sort (created/completed) · r: refresh")
	}

	parts := []string{header, filterLine, body, helpLine}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) emptyBody(noun string) string {
	if m.filter.Value() != "" {
		return mutedStyle.Render("No " + noun + " match filter.")
	}
	return mutedStyle.Render("No " + noun + " found.")
}

func (m Model) levelNoun() string {
	switch m.level {
	case levelGroups:
		return "deployment groups"
	case levelDeployments:
		return "deployments"
	default:
		return "applications"
	}
}

// breadcrumb renders the drill path, e.g. "CodeDeploy › my-app › prod-fleet".
func (m Model) breadcrumb() string {
	parts := []string{"CodeDeploy"}
	if m.level >= levelGroups && m.selApp != "" {
		parts = append(parts, m.selApp)
	}
	if m.level >= levelDeployments && m.selGrp != "" {
		parts = append(parts, m.selGrp)
	}
	if m.level == levelDetail {
		parts = append(parts, m.selDep.ID)
	}
	return strings.Join(parts, " › ")
}

func (m Model) renderDetail() string {
	d := m.selDep
	line := func(k, v string) string {
		if v == "" {
			v = mutedStyle.Render("-")
		}
		return labelStyle.Render(fmt.Sprintf("%-14s", k)) + v
	}
	rows := []string{
		line("Deployment", d.ID),
		line("Status", statusBadge(d.Status)),
		line("Application", d.App),
		line("Group", d.Group),
		line("Config", d.ConfigName),
		line("Creator", d.Creator),
		line("Created", d.Created),
		line("Completed", d.Completed),
		line("Revision", d.Revision),
		line("Instances", fmt.Sprintf("%d succeeded · %d failed · %d in-progress · %d pending · %d skipped",
			d.Succeeded, d.Failed, d.InProgress, d.Pending, d.Skipped)),
	}
	if d.Description != "" {
		rows = append(rows, line("Description", d.Description))
	}
	if d.ErrCode != "" || d.ErrMessage != "" {
		rows = append(rows, errorStyle.Render(fmt.Sprintf("%-14s%s %s", "Error", d.ErrCode, d.ErrMessage)))
	}
	rows = append(rows, "", mutedStyle.Render("esc: back to deployments"))
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// ---------- rows ----------

func appRows(in []App) []datatable.Row {
	rows := make([]datatable.Row, len(in))
	for i, a := range in {
		rows[i] = datatable.Row{a.Name, a.Platform, a.Created}
	}
	return rows
}

func groupRows(in []Group) []datatable.Row {
	rows := make([]datatable.Row, len(in))
	for i, g := range in {
		rows[i] = datatable.Row{g.Name, g.Platform, statusBadge(g.LastStatus), g.LastDeploy}
	}
	return rows
}

func deployRows(in []Deployment) []datatable.Row {
	rows := make([]datatable.Row, len(in))
	for i, d := range in {
		rows[i] = datatable.Row{d.ID, statusBadge(d.Status), d.Creator, d.Created, d.Completed}
	}
	return rows
}

// statusBadge colours a deployment status: Succeeded green, Failed red,
// Stopped muted, transitional states (Created/Queued/InProgress/Baking/Ready)
// yellow. Empty falls through to N/A so the cell is never blank.
func statusBadge(s string) string {
	if s == "" {
		return mutedStyle.Render("N/A")
	}
	style := lipgloss.NewStyle()
	switch s {
	case "Succeeded":
		style = style.Foreground(lipgloss.Color("34"))
	case "Failed":
		style = style.Foreground(lipgloss.Color("160"))
	case "Stopped":
		return mutedStyle.Render(s)
	case "Created", "Queued", "InProgress", "Baking", "Ready":
		style = style.Foreground(lipgloss.Color("214"))
	default:
		return mutedStyle.Render(s)
	}
	return style.Render(s)
}

var (
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle = lipgloss.NewStyle().Bold(true)
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)
