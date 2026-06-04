package beanstalk

import (
	"context"
	"encoding/gob"
	"fmt"
	"sort"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	eb "github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk/types"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/huy-tran/aws-tui/internal/audit"
	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/loader"
	"github.com/huy-tran/aws-tui/internal/ui/statusbar"
)

const envsCacheTTL = 60 * time.Second

func init() {
	gob.Register([]Environment(nil))
	gob.Register([]Version(nil))
}
const versionsCacheTTL = 5 * time.Minute
const envsPollInterval = 10 * time.Second

type Environment struct {
	AppName   string
	EnvName   string
	EnvID     string
	CNAME     string
	Status    string
	Health    string
	Version   string
	Platform  string
	Tier      string
	UpdatedAt string
}

type Event struct {
	Time     time.Time
	Severity string
	Message  string
}

type Version struct {
	Label       string
	Description string
	CreatedAt   string
	Status      string
}

type (
	envsLoadedMsg     struct{ items []Environment }
	eventsLoadedMsg   struct {
		env    string
		events []Event
	}
	versionsLoadedMsg struct {
		app      string
		versions []Version
	}
	deployStartedMsg  struct {
		env    string
		dryRun bool
	}
	tickMsg           struct{}
	errMsg            struct{ err error }
)

type mode int

const (
	modeList mode = iota
	modeDetails
	modeEvents
	modeDeploy
	modeDeployConfirm
)

type Model struct {
	ctx   *awspkg.Context
	mode  mode

	envs       []Environment
	envsFilt   []Environment
	envTable   datatable.Model
	filter     textinput.Model
	filterMode bool
	loading    bool
	polling    bool
	err        error
	status     string

	target   Environment
	events   []Event
	versions []Version

	verTable datatable.Model
	confirm  textinput.Model

	loader     loader.Model
	lastLoaded time.Time

	width, height int
}

func New(ctx *awspkg.Context) Model {
	envCols := []datatable.Column{
		{Title: "App / Environment", Flex: true},
		{Title: "Health", Align: lipgloss.Center},
		{Title: "Status"},
		{Title: "Version"},
	}
	envT := datatable.New(envCols)
	envT.SetHeight(15)

	verCols := []datatable.Column{
		{Title: "Label"},
		{Title: "Created"},
		{Title: "Status"},
		{Title: "Description", Flex: true},
	}
	verT := datatable.New(verCols)
	verT.SetHeight(12)

	ti := textinput.New()
	ti.CharLimit = 64
	ti.Placeholder = "type environment name"

	flt := textinput.New()
	flt.Placeholder = "filter by app / environment name"
	flt.CharLimit = 64
	flt.Prompt = "/ "

	return Model{ctx: ctx, envTable: envT, verTable: verT, confirm: ti, filter: flt, loader: loader.New(), loading: true}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.loadEnvsCmd(false))
}

func (m Model) loadEnvsCmd(force bool) tea.Cmd {
	const key = "eb:environments"
	ctx := m.ctx
	return func() tea.Msg {
		if !force {
			if cached, ok := ctx.Cache.Get(key); ok {
				if items, ok := cached.([]Environment); ok {
					return envsLoadedMsg{items: items}
				}
			}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := ctx.Beanstalk()
		out, err := client.DescribeEnvironments(context.Background(), &eb.DescribeEnvironmentsInput{
			IncludeDeleted: awssdk.Bool(false),
		})
		if err != nil {
			return errMsg{err: err}
		}
		items := make([]Environment, 0, len(out.Environments))
		for _, e := range out.Environments {
			items = append(items, Environment{
				AppName:   awssdk.ToString(e.ApplicationName),
				EnvName:   awssdk.ToString(e.EnvironmentName),
				EnvID:     awssdk.ToString(e.EnvironmentId),
				CNAME:     awssdk.ToString(e.CNAME),
				Status:    string(e.Status),
				Health:    string(e.Health),
				Version:   awssdk.ToString(e.VersionLabel),
				Platform:  awssdk.ToString(e.PlatformArn),
				Tier:      tierName(e.Tier),
				UpdatedAt: formatTime(e.DateUpdated),
			})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].AppName != items[j].AppName {
				return items[i].AppName < items[j].AppName
			}
			return items[i].EnvName < items[j].EnvName
		})
		ctx.Cache.Set(key, items, envsCacheTTL)
		return envsLoadedMsg{items: items}
	}
}

func (m Model) loadEventsCmd(envName string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		client := ctx.Beanstalk()
		out, err := client.DescribeEvents(context.Background(), &eb.DescribeEventsInput{
			EnvironmentName: awssdk.String(envName),
			MaxRecords:      awssdk.Int32(50),
		})
		if err != nil {
			return errMsg{err: err}
		}
		evts := make([]Event, 0, len(out.Events))
		for _, e := range out.Events {
			ts := time.Time{}
			if e.EventDate != nil {
				ts = e.EventDate.Local()
			}
			evts = append(evts, Event{
				Time:     ts,
				Severity: string(e.Severity),
				Message:  awssdk.ToString(e.Message),
			})
		}
		return eventsLoadedMsg{env: envName, events: evts}
	}
}

func (m Model) loadVersionsCmd(appName string) tea.Cmd {
	key := "eb:versions:" + appName
	ctx := m.ctx
	return func() tea.Msg {
		if cached, ok := ctx.Cache.Get(key); ok {
			if vs, ok := cached.([]Version); ok {
				return versionsLoadedMsg{app: appName, versions: vs}
			}
		}
		client := ctx.Beanstalk()
		out, err := client.DescribeApplicationVersions(context.Background(), &eb.DescribeApplicationVersionsInput{
			ApplicationName: awssdk.String(appName),
		})
		if err != nil {
			return errMsg{err: err}
		}
		vs := make([]Version, 0, len(out.ApplicationVersions))
		for _, v := range out.ApplicationVersions {
			vs = append(vs, Version{
				Label:       awssdk.ToString(v.VersionLabel),
				Description: awssdk.ToString(v.Description),
				CreatedAt:   formatTime(v.DateCreated),
				Status:      string(v.Status),
			})
		}
		sort.Slice(vs, func(i, j int) bool { return vs[i].CreatedAt > vs[j].CreatedAt })
		ctx.Cache.Set(key, vs, versionsCacheTTL)
		return versionsLoadedMsg{app: appName, versions: vs}
	}
}

func (m Model) deployCmd(envName, versionLabel string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		action := audit.Action{
			Profile: ctx.Profile,
			Region:  ctx.Region,
			Action:  "elasticbeanstalk:UpdateEnvironment",
			Target:  envName,
			Payload: map[string]any{"version": versionLabel},
		}
		if audit.IsDryRun() {
			audit.Log(action, true, "")
			ctx.Cache.Invalidate("eb:environments")
			return deployStartedMsg{env: envName, dryRun: true}
		}
		client := ctx.Beanstalk()
		_, err := client.UpdateEnvironment(context.Background(), &eb.UpdateEnvironmentInput{
			EnvironmentName: awssdk.String(envName),
			VersionLabel:    awssdk.String(versionLabel),
		})
		if err != nil {
			return errMsg{err: err}
		}
		audit.Log(action, false, versionLabel)
		ctx.Cache.Invalidate("eb:environments")
		return deployStartedMsg{env: envName}
	}
}

func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 6
		if h < 3 {
			h = 3
		}
		m.envTable.SetHeight(h)
		m.envTable.SetWidth(msg.Width)
		m.verTable.SetHeight(h - 6)
		m.verTable.SetWidth(msg.Width)
		return m, nil

	case envsLoadedMsg:
		m.loading = false
		m.envs = msg.items
		m.lastLoaded = time.Now()
		m.applyEnvFilter()
		if hasTransitioning(m.envs) && !m.polling {
			m.polling = true
			return m, tickEvery(envsPollInterval)
		}
		if !hasTransitioning(m.envs) {
			m.polling = false
		}
		return m, nil

	case eventsLoadedMsg:
		if msg.env == m.target.EnvName {
			m.events = msg.events
		}
		return m, nil

	case versionsLoadedMsg:
		if msg.app == m.target.AppName {
			m.versions = msg.versions
			m.verTable.SetRows(buildVersionRows(m.versions))
		}
		return m, nil

	case deployStartedMsg:
		if msg.dryRun {
			m.status = "dry-run: deploy on " + msg.env
		} else {
			m.status = "Deploy started on " + msg.env
		}
		m.mode = modeList
		m.loading = true
		return m, tea.Batch(m.loader.Tick(), m.loadEnvsCmd(true))

	case tickMsg:
		if m.polling && m.mode == modeList {
			return m, tea.Batch(m.loadEnvsCmd(true), tickEvery(envsPollInterval))
		}
		return m, nil

	case errMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case spinner.TickMsg:
		if !m.loading {
			return m, nil
		}
		var cmd tea.Cmd
		m.loader, cmd = m.loader.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.updateKeys(msg)
	}
	return m, nil
}

func (m Model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeDetails:
		switch msg.String() {
		case "esc":
			m.mode = modeList
			return m, nil
		case "e":
			m.mode = modeEvents
			return m, m.loadEventsCmd(m.target.EnvName)
		case "d":
			m.mode = modeDeploy
			return m, m.loadVersionsCmd(m.target.AppName)
		}
		return m, nil

	case modeEvents:
		switch msg.String() {
		case "esc":
			m.mode = modeDetails
			return m, nil
		case "r":
			return m, m.loadEventsCmd(m.target.EnvName)
		}
		return m, nil

	case modeDeploy:
		switch msg.String() {
		case "esc":
			m.mode = modeDetails
			return m, nil
		case "r":
			m.ctx.Cache.Invalidate("eb:versions:" + m.target.AppName)
			return m, m.loadVersionsCmd(m.target.AppName)
		case "enter":
			if v := m.selectedVersion(); v != nil {
				if isProdEnv(m.target.EnvName) {
					m.mode = modeDeployConfirm
					m.confirm.SetValue("")
					m.confirm.Focus()
					m.status = "type environment name to confirm prod deploy"
					return m, textinput.Blink
				}
				return m, m.deployCmd(m.target.EnvName, v.Label)
			}
		}
		var cmd tea.Cmd
		m.verTable, cmd = m.verTable.Update(msg)
		return m, cmd

	case modeDeployConfirm:
		switch msg.String() {
		case "esc":
			m.mode = modeDeploy
			m.status = ""
			return m, nil
		case "enter":
			if m.confirm.Value() == m.target.EnvName {
				v := m.selectedVersion()
				if v == nil {
					return m, nil
				}
				m.status = ""
				return m, m.deployCmd(m.target.EnvName, v.Label)
			}
			m.status = "name mismatch - try again or esc"
			return m, nil
		}
		var cmd tea.Cmd
		m.confirm, cmd = m.confirm.Update(msg)
		return m, cmd

	default: // modeList
		if m.filterMode {
			switch msg.String() {
			case "esc":
				m.filterMode = false
				m.filter.SetValue("")
				m.filter.Blur()
				m.applyEnvFilter()
				return m, nil
			case "enter":
				m.filterMode = false
				m.filter.Blur()
				return m, nil
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.applyEnvFilter()
			return m, cmd
		}
		switch msg.String() {
		case "/":
			m.filterMode = true
			m.filter.Focus()
			return m, textinput.Blink
		case "r":
			m.ctx.Cache.Invalidate("eb:environments")
			m.loading = true
			return m, tea.Batch(m.loader.Tick(), m.loadEnvsCmd(true))
		case "enter":
			if e := m.selectedEnv(); e != nil {
				m.target = *e
				m.mode = modeDetails
				return m, m.loadEventsCmd(e.EnvName)
			}
		case "e":
			if e := m.selectedEnv(); e != nil {
				m.target = *e
				m.mode = modeEvents
				return m, m.loadEventsCmd(e.EnvName)
			}
		case "d":
			if e := m.selectedEnv(); e != nil {
				m.target = *e
				m.mode = modeDeploy
				return m, m.loadVersionsCmd(e.AppName)
			}
		}
		var cmd tea.Cmd
		m.envTable, cmd = m.envTable.Update(msg)
		return m, cmd
	}
}

func (m *Model) applyEnvFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		m.envsFilt = m.envs
	} else {
		out := make([]Environment, 0, len(m.envs))
		for _, e := range m.envs {
			if strings.Contains(strings.ToLower(e.AppName), q) ||
				strings.Contains(strings.ToLower(e.EnvName), q) ||
				strings.Contains(strings.ToLower(e.CNAME), q) {
				out = append(out, e)
			}
		}
		m.envsFilt = out
	}
	m.envTable.SetRows(buildEnvRows(m.envsFilt))
}

func (m Model) CapturingInput() bool {
	if m.mode == modeList && m.filterMode {
		return true
	}
	return m.mode == modeDeployConfirm
}

func (m Model) InSubnav() bool {
	return m.mode != modeList
}

func (m Model) BookmarkCurrent() (string, string, bool) {
	e := m.selectedEnv()
	if e == nil {
		return "", "", false
	}
	label := e.AppName + " / " + e.EnvName
	return label, e.EnvName, true
}

func (m Model) SetFilterQuery(q string) tea.Model {
	m.filter.SetValue(q)
	m.applyEnvFilter()
	return m
}

func (m Model) StatusFooter() statusbar.Snapshot {
	return statusbar.Snapshot{
		Items:      len(m.envsFilt),
		LastLoaded: m.lastLoaded,
		Message:    m.status,
	}
}

func (m Model) HelpItems() []help.Section {
	switch m.mode {
	case modeDetails:
		return []help.Section{{
			Title: "Beanstalk · details",
			Items: []help.Item{
				{Keys: "e", Desc: "full events"},
				{Keys: "d", Desc: "deploy"},
				{Keys: "esc", Desc: "back to environments"},
			},
		}}
	case modeEvents:
		return []help.Section{{
			Title: "Beanstalk · events",
			Items: []help.Item{
				{Keys: "r", Desc: "refresh"},
				{Keys: "esc", Desc: "back to details"},
			},
		}}
	case modeDeploy:
		return []help.Section{{
			Title: "Beanstalk · deploy",
			Items: []help.Item{
				{Keys: "enter", Desc: "deploy selected version (prod needs name-typed confirm)"},
				{Keys: "r", Desc: "refresh versions"},
				{Keys: "esc", Desc: "back to details"},
			},
		}}
	case modeDeployConfirm:
		return []help.Section{{
			Title: "Beanstalk · confirm prod deploy",
			Items: []help.Item{
				{Keys: "type env name", Desc: "must match exactly"},
				{Keys: "enter", Desc: "deploy"},
				{Keys: "esc", Desc: "cancel"},
			},
		}}
	}
	if m.filterMode {
		return []help.Section{{
			Title: "Beanstalk · filter",
			Items: []help.Item{
				{Keys: "type", Desc: "match app / environment / CNAME"},
				{Keys: "enter", Desc: "apply and exit filter"},
				{Keys: "esc", Desc: "clear and exit filter"},
			},
		}}
	}
	return []help.Section{{
		Title: "Beanstalk environments",
		Items: []help.Item{
			{Keys: "enter", Desc: "open details"},
			{Keys: "e", Desc: "events for env"},
			{Keys: "d", Desc: "deploy a version"},
			{Keys: "/", Desc: "filter"},
			{Keys: "r", Desc: "refresh"},
		},
	}}
}

func (m Model) selectedEnv() *Environment {
	if m.loading || len(m.envsFilt) == 0 {
		return nil
	}
	idx := m.envTable.Cursor()
	if idx < 0 || idx >= len(m.envsFilt) {
		return nil
	}
	return &m.envsFilt[idx]
}

func (m Model) selectedVersion() *Version {
	if len(m.versions) == 0 {
		return nil
	}
	idx := m.verTable.Cursor()
	if idx < 0 || idx >= len(m.versions) {
		return nil
	}
	return &m.versions[idx]
}

func (m Model) View() string {
	if m.err != nil {
		hint := "Press r to retry · esc to back."
		if _, ok := m.err.(*awspkg.SSOExpiredError); ok {
			hint = "Run `aws sso login --profile " + m.ctx.Profile + "` then press r."
		}
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\n%s", m.err, hint))
	}
	switch m.mode {
	case modeDetails:
		return m.viewDetails()
	case modeEvents:
		return m.viewEvents()
	case modeDeploy:
		return m.viewDeploy()
	case modeDeployConfirm:
		return m.viewDeployConfirm()
	}
	if m.loading {
		return m.loader.Render("loading environments...")
	}
	if len(m.envs) == 0 {
		return mutedStyle.Render("No Beanstalk environments in this region.")
	}
	header := headerStyle.Render(fmt.Sprintf("Elastic Beanstalk Environments (%d)", len(m.envs)))
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}
	body := m.envTable.View()
	if len(m.envsFilt) == 0 && m.filter.Value() != "" {
		body = mutedStyle.Render("No environments match filter.")
	}
	help := mutedStyle.Render("enter: details · e: events · d: deploy · /: filter · r: refresh")
	parts := []string{header}
	if m.polling {
		parts = append(parts, mutedStyle.Render("auto-refresh every 10s while any env is in transition"))
	}
	parts = append(parts, filterLine, "", body, "", help)
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) viewDetails() string {
	title := headerStyle.Render(m.target.EnvName)
	rows := []string{
		field("Application", m.target.AppName),
		field("Environment ID", m.target.EnvID),
		field("CNAME", m.target.CNAME),
		field("Status", statusBadge(m.target.Status)),
		field("Health", healthDot(m.target.Health)),
		field("Version", m.target.Version),
		field("Tier", m.target.Tier),
		field("Last updated", m.target.UpdatedAt),
	}
	body := strings.Join(rows, "\n")
	evts := mutedStyle.Render("(no recent events loaded)")
	if len(m.events) > 0 {
		var sb strings.Builder
		sb.WriteString(headerStyle.Render("Recent events"))
		sb.WriteString("\n")
		for i, e := range m.events {
			if i >= 5 {
				break
			}
			sb.WriteString("  ")
			sb.WriteString(formatEvent(e))
			sb.WriteString("\n")
		}
		evts = sb.String()
	}
	help := mutedStyle.Render("e: full events · d: deploy · esc: back")
	return lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", evts, "", help)
}

func (m Model) viewEvents() string {
	title := headerStyle.Render("Events: " + m.target.EnvName)
	if len(m.events) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left, title, "", mutedStyle.Render("loading events..."))
	}
	var sb strings.Builder
	for _, e := range m.events {
		sb.WriteString(formatEvent(e))
		sb.WriteString("\n")
	}
	help := mutedStyle.Render("r: refresh · esc: back")
	return lipgloss.JoinVertical(lipgloss.Left, title, "", sb.String(), "", help)
}

func (m Model) viewDeploy() string {
	title := headerStyle.Render("Deploy to " + m.target.EnvName)
	current := mutedStyle.Render("Currently deployed: " + m.target.Version)
	if len(m.versions) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left, title, current, "", mutedStyle.Render("loading versions..."))
	}
	help := mutedStyle.Render("enter: deploy selected · r: refresh · esc: cancel")
	return lipgloss.JoinVertical(lipgloss.Left, title, current, "", m.verTable.View(), "", help)
}

func (m Model) viewDeployConfirm() string {
	title := headerStyle.Render("CONFIRM PROD DEPLOY")
	v := m.selectedVersion()
	versionLabel := "-"
	if v != nil {
		versionLabel = v.Label
	}
	body := fmt.Sprintf("Environment: %s\nVersion:     %s\n\nType the environment name to confirm:",
		m.target.EnvName, versionLabel)
	status := ""
	if m.status != "" {
		status = errorStyle.Render(m.status)
	}
	help := mutedStyle.Render("enter to deploy · esc to cancel")
	return lipgloss.JoinVertical(lipgloss.Left, title, "", body, m.confirm.View(), "", status, "", help)
}

func buildEnvRows(envs []Environment) []datatable.Row {
	rows := make([]datatable.Row, len(envs))
	for i, e := range envs {
		name := e.AppName + " / " + e.EnvName
		rows[i] = datatable.Row{name, healthDot(e.Health), statusBadge(e.Status), e.Version}
	}
	return rows
}

func buildVersionRows(versions []Version) []datatable.Row {
	rows := make([]datatable.Row, len(versions))
	for i, v := range versions {
		rows[i] = datatable.Row{v.Label, v.CreatedAt, v.Status, v.Description}
	}
	return rows
}

func formatEvent(e Event) string {
	ts := e.Time.Format("15:04:05")
	sev := strings.ToUpper(e.Severity)
	style := mutedStyle
	switch sev {
	case "WARN":
		style = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	case "ERROR":
		style = errorStyle
	}
	return fmt.Sprintf("%s  %s  %s", ts, style.Render(padRight(sev, 7)), e.Message)
}

func healthDot(color string) string {
	if color == "" {
		return mutedStyle.Render("N/A")
	}
	style := lipgloss.NewStyle().Bold(true)
	switch color {
	case "Green", "Ok":
		style = style.Foreground(lipgloss.Color("34"))
	case "Yellow", "Warning":
		style = style.Foreground(lipgloss.Color("214"))
	case "Red", "Severe", "Degraded":
		style = style.Foreground(lipgloss.Color("160"))
	default:
		style = style.Foreground(lipgloss.Color("245"))
	}
	return style.Render("● " + color)
}

// statusBadge mirrors healthDot for the Status column. The Beanstalk
// EnvironmentStatus enum's transitional values (Launching / Updating /
// Linking*) are warm-yellow; terminal states (Terminating / Terminated /
// Aborting) are red; Ready is green. Anything unrecognised falls through
// muted, and an empty string renders as N/A so the column never looks
// "blank but populated".
func statusBadge(status string) string {
	if status == "" {
		return mutedStyle.Render("N/A")
	}
	style := lipgloss.NewStyle()
	switch status {
	case "Ready":
		style = style.Foreground(lipgloss.Color("34"))
	case "Launching", "Updating", "LinkingFrom", "LinkingTo":
		style = style.Foreground(lipgloss.Color("214"))
	case "Aborting", "Terminating", "Terminated":
		style = style.Foreground(lipgloss.Color("160"))
	default:
		return mutedStyle.Render(status)
	}
	return style.Render(status)
}

func isProdEnv(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "prod")
}

func tierName(t *ebtypes.EnvironmentTier) string {
	if t == nil {
		return ""
	}
	return awssdk.ToString(t.Name)
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

func field(label, value string) string {
	return fmt.Sprintf("  %-16s %s", label+":", value)
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func hasTransitioning(envs []Environment) bool {
	for _, e := range envs {
		switch e.Status {
		case "Updating", "Launching", "Terminating":
			return true
		}
	}
	return false
}

var (
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle = lipgloss.NewStyle().Bold(true)
)
