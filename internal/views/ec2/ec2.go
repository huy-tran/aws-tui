package ec2

import (
	"context"
	"encoding/gob"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/huy-tran/aws-tui/internal/audit"
	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/loader"
	"github.com/huy-tran/aws-tui/internal/ui/statusbar"
)

const instancesCacheTTL = 60 * time.Second

// stateFilterAll is the sentinel value that disables state filtering.
const stateFilterAll = "all"

// stateFilters is the cycle of EC2 state filters toggled with 'f'. Index 0
// ("all") shows everything; the rest match a single instance state exactly.
// The default selection is "running" (see New).
var stateFilters = []string{
	stateFilterAll,
	"running",
	"pending",
	"stopping",
	"stopped",
	"shutting-down",
	"terminated",
}

// stateFilterIndex returns the position of s in stateFilters, or 0 ("all")
// when it is not found.
func stateFilterIndex(s string) int {
	for i, v := range stateFilters {
		if v == s {
			return i
		}
	}
	return 0
}

func init() { gob.Register([]Instance(nil)) }

// TagPair preserves both key and value for filtering and the details panel.
type TagPair struct {
	Key   string
	Value string
}

type Instance struct {
	ID             string
	Name           string
	State          string
	Type           string
	PrivIP         string
	PubIP          string
	PubDNS         string
	VPCID          string
	SubnetID       string
	AZ             string
	Platform       string
	AMIID          string
	IAMRole        string
	LaunchTime     string
	SecurityGroups []string
	Tags           []TagPair
}

type loadedMsg struct{ instances []Instance }
type errMsg struct{ err error }

// powerAction is the verb passed to the ec2 SDK for stop/start/reboot.
// Stored as a string so it doubles as the audit action suffix and the
// label in the confirmation prompt.
type powerAction string

const (
	actionStop   powerAction = "stop"
	actionStart  powerAction = "start"
	actionReboot powerAction = "reboot"
)

// powerDoneMsg comes back from the goroutine that runs the SDK call. err
// non-nil = the API rejected the request; dryRun = audit dry-run mode
// intercepted the call before it hit AWS.
type powerDoneMsg struct {
	action powerAction
	target string
	err    error
	dryRun bool
}

type Model struct {
	ctx        *awspkg.Context
	table      datatable.Model
	loader     loader.Model
	filter     textinput.Model
	filterMode bool
	instances  []Instance // full list as returned by DescribeInstances
	displayed  []Instance // visible after filter; equals instances when no filter
	stateFilterIdx int    // index into stateFilters; selects which states show
	statePicking   bool   // true while the "press a state number" ribbon is up
	loading    bool
	err        error
	width      int
	height     int
	lastLoaded time.Time
	status     string

	// Power action confirmation state. pendingAction != "" means the
	// list view is overlayed by a confirm prompt waiting on the user
	// to type the target instance ID. Inflight is true once the SDK
	// call is on the wire; the spinner shares loader.
	pendingAction powerAction
	pendingTarget Instance
	confirmInput  textinput.Model
	actionInFlight bool
}

func New(ctx *awspkg.Context) Model {
	columns := []datatable.Column{
		{Title: "Name", Flex: true},
		{Title: "Instance ID"},
		{Title: "State"},
		{Title: "Type"},
		{Title: "Priv IP"},
	}
	t := datatable.New(columns)
	t.SetHeight(20)

	ti := textinput.New()
	ti.Placeholder = "filter by name / id / ip / tag"
	ti.CharLimit = 64
	ti.Prompt = "/ "

	confirm := textinput.New()
	confirm.Placeholder = "type the instance id to confirm"
	confirm.CharLimit = 32

	return Model{
		ctx:            ctx,
		table:          t,
		loader:         loader.New(),
		filter:         ti,
		confirmInput:   confirm,
		stateFilterIdx: stateFilterIndex("running"),
		loading:        true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.loadCmd(false))
}

// loadCmd returns the load command. When force is true, the cache is bypassed
// and a refresh is forced (used by the 'r' key).
func (m Model) loadCmd(force bool) tea.Cmd {
	cacheKey := "ec2:instances:" + m.ctx.Region
	ctx := m.ctx
	return func() tea.Msg {
		if !force {
			if cached, ok := ctx.Cache.Get(cacheKey); ok {
				if instances, ok := cached.([]Instance); ok {
					return loadedMsg{instances: instances}
				}
			}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := ctx.EC2()
		var instances []Instance
		paginator := ec2sdk.NewDescribeInstancesPaginator(client, &ec2sdk.DescribeInstancesInput{})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(context.Background())
			if err != nil {
				return errMsg{err: err}
			}
			for _, res := range page.Reservations {
				for _, inst := range res.Instances {
					instances = append(instances, parseInstance(inst))
				}
			}
		}
		ctx.Cache.Set(cacheKey, instances, instancesCacheTTL)
		return loadedMsg{instances: instances}
	}
}

// sortInstancesByName orders instances by their Name tag (case-insensitive),
// with the instance ID as a tie-breaker. Unnamed instances sort after named
// ones so they cluster at the bottom of the list.
func sortInstancesByName(instances []Instance) {
	sort.SliceStable(instances, func(i, j int) bool {
		ni, nj := instances[i].Name, instances[j].Name
		if (ni == "") != (nj == "") {
			return ni != "" // named instances first
		}
		li, lj := strings.ToLower(ni), strings.ToLower(nj)
		if li != lj {
			return li < lj
		}
		return instances[i].ID < instances[j].ID
	})
}

func parseInstance(inst types.Instance) Instance {
	out := Instance{
		ID:     awssdk.ToString(inst.InstanceId),
		Type:   string(inst.InstanceType),
		PrivIP: awssdk.ToString(inst.PrivateIpAddress),
		PubIP:  awssdk.ToString(inst.PublicIpAddress),
		PubDNS: awssdk.ToString(inst.PublicDnsName),
		VPCID:  awssdk.ToString(inst.VpcId),
		SubnetID: awssdk.ToString(inst.SubnetId),
		AMIID:    awssdk.ToString(inst.ImageId),
	}
	if inst.State != nil {
		out.State = string(inst.State.Name)
	}
	if inst.Placement != nil {
		out.AZ = awssdk.ToString(inst.Placement.AvailabilityZone)
	}
	if inst.LaunchTime != nil {
		out.LaunchTime = inst.LaunchTime.Format(time.RFC3339)
	}
	if inst.IamInstanceProfile != nil {
		// ARN is like arn:aws:iam::123:instance-profile/Foo
		arn := awssdk.ToString(inst.IamInstanceProfile.Arn)
		if idx := strings.LastIndex(arn, "/"); idx >= 0 {
			out.IAMRole = arn[idx+1:]
		} else {
			out.IAMRole = arn
		}
	}
	if inst.Platform == types.PlatformValuesWindows {
		out.Platform = "windows"
	} else {
		out.Platform = "linux"
	}
	for _, sg := range inst.SecurityGroups {
		out.SecurityGroups = append(out.SecurityGroups, fmt.Sprintf("%s (%s)",
			awssdk.ToString(sg.GroupName), awssdk.ToString(sg.GroupId)))
	}
	for _, tag := range inst.Tags {
		k, v := awssdk.ToString(tag.Key), awssdk.ToString(tag.Value)
		out.Tags = append(out.Tags, TagPair{Key: k, Value: v})
		if k == "Name" {
			out.Name = v
		}
	}
	return out
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Reserve a row for the filter input plus a row for help text.
		tableHeight := msg.Height - 4
		if tableHeight < 3 {
			tableHeight = 3
		}
		m.table.SetHeight(tableHeight)
		m.table.SetWidth(msg.Width)
		return m, nil

	case loadedMsg:
		m.loading = false
		m.instances = msg.instances
		sortInstancesByName(m.instances)
		m.lastLoaded = time.Now()
		m.applyFilter()
		return m, nil

	case errMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case powerDoneMsg:
		m.actionInFlight = false
		m.pendingAction = ""
		m.confirmInput.SetValue("")
		m.confirmInput.Blur()
		if msg.err != nil {
			m.status = fmt.Sprintf("%s %s failed: %v", msg.action, msg.target, msg.err)
			return m, nil
		}
		if msg.dryRun {
			m.status = fmt.Sprintf("dry-run: %s %s", msg.action, msg.target)
		} else {
			m.status = fmt.Sprintf("%s requested for %s", msg.action, msg.target)
		}
		// Bust the cache and refresh so the State column reflects the change.
		m.ctx.Cache.Invalidate("ec2:instances:" + m.ctx.Region)
		m.loading = true
		return m, tea.Batch(m.loader.Tick(), m.loadCmd(true))

	case tea.KeyMsg:
		// Power action confirm prompt: typed-ID confirmation pattern,
		// mirrors paramstore's prod-write gate.
		if m.pendingAction != "" {
			switch msg.String() {
			case "esc":
				m.pendingAction = ""
				m.confirmInput.SetValue("")
				m.confirmInput.Blur()
				m.status = ""
				return m, nil
			case "enter":
				if m.confirmInput.Value() != m.pendingTarget.ID {
					m.status = "instance id mismatch - try again or esc"
					return m, nil
				}
				m.actionInFlight = true
				m.status = fmt.Sprintf("%s requested...", m.pendingAction)
				return m, tea.Batch(m.loader.Tick(), m.powerActionCmd(m.pendingAction, m.pendingTarget))
			}
			var cmd tea.Cmd
			m.confirmInput, cmd = m.confirmInput.Update(msg)
			return m, cmd
		}
		// State-filter picker ribbon: consume the next keystroke. Digit
		// 1..N or the state's first letter jumps straight to it; esc cancels.
		if m.statePicking {
			return m.handleStatePick(msg), nil
		}
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

		switch msg.String() {
		case "r":
			m.ctx.Cache.Invalidate("ec2:instances:" + m.ctx.Region)
			m.loading = true
			m.err = nil
			return m, tea.Batch(m.loader.Tick(), m.loadCmd(true))
		case "/":
			m.filterMode = true
			m.filter.Focus()
			return m, textinput.Blink
		case "f":
			m.statePicking = true
			return m, nil
		case "enter":
			if inst := m.selected(); inst != nil && inst.State == "running" {
				return m, m.startSSMSession(*inst)
			}
		case "p":
			if inst := m.selected(); inst != nil && inst.State == "running" {
				return m, nav.PushView(newPortForward(m.ctx, *inst))
			}
		case "i":
			if inst := m.selected(); inst != nil {
				return m, nav.PushView(newDetails(m.ctx, *inst))
			}
		case "S":
			if inst := m.selected(); inst != nil {
				m.pendingAction = actionStop
				m.pendingTarget = *inst
				m.confirmInput.SetValue("")
				m.confirmInput.Focus()
				m.status = ""
				return m, textinput.Blink
			}
		case "U":
			if inst := m.selected(); inst != nil {
				m.pendingAction = actionStart
				m.pendingTarget = *inst
				m.confirmInput.SetValue("")
				m.confirmInput.Focus()
				m.status = ""
				return m, textinput.Blink
			}
		case "R":
			if inst := m.selected(); inst != nil {
				m.pendingAction = actionReboot
				m.pendingTarget = *inst
				m.confirmInput.SetValue("")
				m.confirmInput.Focus()
				m.status = ""
				return m, textinput.Blink
			}
		case "c":
			if inst := m.selected(); inst != nil {
				return m, nav.PushView(newConsole(m.ctx, *inst))
			}
		}
	}

	var cmd tea.Cmd
	if m.loading {
		if _, ok := msg.(spinner.TickMsg); ok {
			m.loader, cmd = m.loader.Update(msg)
			return m, cmd
		}
	} else {
		m.table, cmd = m.table.Update(msg)
	}
	return m, cmd
}

// stateFilter returns the currently selected state filter ("all" disables
// state filtering).
func (m Model) stateFilter() string { return stateFilters[m.stateFilterIdx] }

// handleStatePick consumes the next key while the state-filter ribbon is up,
// mirroring the datatable sort picker. Digit 1..N selects that state; a
// single letter selects the first state whose name starts with it (so 'a'
// all, 'r' running, 'p' pending, 't' terminated; 's' picks the first of the
// s-states); esc cancels. Anything else just closes the ribbon.
func (m Model) handleStatePick(msg tea.KeyMsg) Model {
	m.statePicking = false
	s := msg.String()
	if s == "esc" {
		return m
	}
	target := -1
	if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(stateFilters) {
		target = n - 1
	} else if len(s) == 1 {
		ch := strings.ToLower(s)
		for i, st := range stateFilters {
			if strings.HasPrefix(st, ch) {
				target = i
				break
			}
		}
	}
	if target < 0 {
		return m
	}
	m.stateFilterIdx = target
	m.applyFilter()
	return m
}

// renderStateRibbon shows the numbered states the user can press while the
// state-filter picker is open.
func (m Model) renderStateRibbon() string {
	parts := make([]string, len(stateFilters))
	for i, st := range stateFilters {
		parts[i] = fmt.Sprintf("[%d] %s", i+1, st)
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	return style.Render("filter state: ") + strings.Join(parts, "  ") +
		mutedStyle.Render("   (esc to cancel)")
}

// applyFilter rebuilds m.displayed and the table rows from the current state
// filter and filter text. The state filter (default "running") narrows by
// instance state; the text filter narrows by name / id / ip / tag. Always
// allocates a fresh slice so it never aliases m.instances - aliasing
// previously caused duplicate / corrupted rows when filters were tightened
// in steps.
func (m *Model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	state := m.stateFilter()
	out := make([]Instance, 0, len(m.instances))
	for _, inst := range m.instances {
		if state != stateFilterAll && inst.State != state {
			continue
		}
		if q != "" && !instanceMatches(inst, q) {
			continue
		}
		out = append(out, inst)
	}
	m.displayed = out
	m.table.SetRows(buildRows(m.displayed))
}

func instanceMatches(inst Instance, q string) bool {
	if strings.Contains(strings.ToLower(inst.Name), q) ||
		strings.Contains(strings.ToLower(inst.ID), q) ||
		strings.Contains(strings.ToLower(inst.PrivIP), q) ||
		strings.Contains(strings.ToLower(inst.PubIP), q) {
		return true
	}
	for _, t := range inst.Tags {
		if strings.Contains(strings.ToLower(t.Value), q) {
			return true
		}
	}
	return false
}

func (m Model) CapturingInput() bool {
	return m.filterMode || m.pendingAction != "" || m.statePicking
}

// BookmarkCurrent surfaces the highlighted instance so the dashboard can
// persist it. Returns ok=false when nothing is selectable.
func (m Model) BookmarkCurrent() (string, string, bool) {
	inst := m.selected()
	if inst == nil {
		return "", "", false
	}
	label := inst.Name
	if label == "" {
		label = inst.ID
	} else {
		label = fmt.Sprintf("%s (%s)", label, inst.ID)
	}
	return label, inst.ID, true
}

// SetFilterQuery is called by the dashboard when the user jumps via a
// bookmark; pre-fills the filter so the bookmarked row lands in view.
// Returns the updated model because the tab is stored by value in the
// dashboard's tea.Model slice.
func (m Model) SetFilterQuery(q string) tea.Model {
	m.filter.SetValue(q)
	// Drop to "all" so a bookmarked instance in any state (stopped,
	// terminated, ...) still lands in view instead of being hidden by the
	// default running-only filter.
	m.stateFilterIdx = stateFilterIndex(stateFilterAll)
	m.applyFilter()
	return m
}

func (m Model) StatusFooter() statusbar.Snapshot {
	return statusbar.Snapshot{
		Items:      len(m.displayed),
		LastLoaded: m.lastLoaded,
		Message:    m.status,
	}
}

func (m Model) HelpItems() []help.Section {
	if m.filterMode {
		return []help.Section{{
			Title: "EC2 · filter",
			Items: []help.Item{
				{Keys: "type", Desc: "match name / id / private IP / public IP / tag value"},
				{Keys: "enter", Desc: "apply and exit filter (keeps the query)"},
				{Keys: "esc", Desc: "clear and exit filter"},
			},
		}}
	}
	if m.statePicking {
		return []help.Section{{
			Title: "EC2 · state filter",
			Items: []help.Item{
				{Keys: "1-7", Desc: "pick state by number (all / running / … / terminated)"},
				{Keys: "a r p s t", Desc: "first letter jumps to that state"},
				{Keys: "esc", Desc: "cancel"},
			},
		}}
	}
	if m.pendingAction != "" {
		return []help.Section{{
			Title: fmt.Sprintf("EC2 · confirm %s", m.pendingAction),
			Items: []help.Item{
				{Keys: "type instance id", Desc: "must match exactly"},
				{Keys: "enter", Desc: "run the action"},
				{Keys: "esc", Desc: "cancel"},
			},
		}}
	}
	return []help.Section{{
		Title: "EC2 instances",
		Items: []help.Item{
			{Keys: "enter", Desc: "SSM start-session into selected instance (bash)"},
			{Keys: "p", Desc: "port-forward modal"},
			{Keys: "i", Desc: "instance details"},
			{Keys: "c", Desc: "console output"},
			{Keys: "S / U / R", Desc: "stop / start (up) / reboot (confirm required)"},
			{Keys: "f", Desc: "pick state filter (ribbon: 1-7 or first letter)"},
			{Keys: "/", Desc: "filter"},
			{Keys: "r", Desc: "refresh (invalidates cache)"},
			{Keys: "↑/↓ j/k", Desc: "move cursor"},
			{Keys: "pgup/pgdn", Desc: "page"},
		},
	}}
}

func (m Model) selected() *Instance {
	if m.loading || len(m.displayed) == 0 {
		return nil
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.displayed) {
		return nil
	}
	return &m.displayed[idx]
}

// powerActionCmd dispatches Stop/Start/RebootInstances for the target
// instance, routed through audit. Returns a powerDoneMsg so the Update
// handler can resync the cache and surface the outcome.
func (m Model) powerActionCmd(action powerAction, target Instance) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		sdkAction := "ec2:" + strings.Title(string(action)) + "Instances"
		a := audit.Action{
			Profile: ctx.Profile,
			Region:  ctx.Region,
			Action:  sdkAction,
			Target:  target.ID,
			Payload: map[string]any{"name": target.Name, "state_before": target.State},
		}
		if audit.IsDryRun() {
			audit.Log(a, true, "")
			return powerDoneMsg{action: action, target: target.ID, dryRun: true}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return powerDoneMsg{action: action, target: target.ID, err: err}
		}
		client := ctx.EC2()
		ids := []string{target.ID}
		var err error
		switch action {
		case actionStop:
			_, err = client.StopInstances(context.Background(), &ec2sdk.StopInstancesInput{InstanceIds: ids})
		case actionStart:
			_, err = client.StartInstances(context.Background(), &ec2sdk.StartInstancesInput{InstanceIds: ids})
		case actionReboot:
			_, err = client.RebootInstances(context.Background(), &ec2sdk.RebootInstancesInput{InstanceIds: ids})
		default:
			err = fmt.Errorf("unknown action %q", action)
		}
		audit.Log(a, false, "")
		return powerDoneMsg{action: action, target: target.ID, err: err}
	}
}

func (m Model) startSSMSession(inst Instance) tea.Cmd {
	// Default SSM sessions drop into /bin/sh on Amazon Linux which is
	// missing the niceties most operators expect (line editing, history,
	// completion). Use AWS-StartInteractiveCommand to start /bin/bash
	// directly so users don't have to type `exec /bin/bash` after every
	// connect. Falls back gracefully on hosts where /bin/bash is missing
	// - AWS CLI surfaces the failure and we propagate it.
	cmd := execAWS("ssm", "start-session",
		"--profile", m.ctx.Profile,
		"--region", m.ctx.Region,
		"--target", inst.ID,
		"--document-name", "AWS-StartInteractiveCommand",
		"--parameters", "command=/bin/bash",
	)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return errMsg{err: fmt.Errorf("ssm session failed: %w", err)}
		}
		return nil
	})
}

func (m Model) View() string {
	if m.err != nil {
		hint := "Press r to retry."
		if _, ok := m.err.(*awspkg.SSOExpiredError); ok {
			hint = "Run `aws sso login --profile " + m.ctx.Profile + "` in another shell, then press r."
		}
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\n%s", m.err, hint))
	}
	if m.loading {
		return m.loader.Render("loading instances...")
	}

	header := m.renderListHeader()
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}
	if m.statePicking {
		filterLine = m.renderStateRibbon()
	}

	help := mutedStyle.Render("enter: SSM · p: port forward · i: details · c: console · S/U/R: stop/start/reboot · f: state · r: refresh · /: filter")

	body := m.table.View()
	if len(m.displayed) == 0 {
		switch {
		case m.filter.Value() != "":
			body = mutedStyle.Render("No instances match filter.")
		case m.stateFilter() != stateFilterAll && len(m.instances) > 0:
			body = mutedStyle.Render(fmt.Sprintf("No %s instances (press f to change state filter).", m.stateFilter()))
		default:
			body = mutedStyle.Render("No instances in this region.")
		}
	}

	parts := []string{header, filterLine, body, help}
	if m.pendingAction != "" {
		// Confirmation overlay (rendered inline below the list). Border
		// makes it obvious the focus moved here and esc/enter are the
		// only meaningful keys.
		prompt := fmt.Sprintf("Confirm %s of %s", m.pendingAction, m.pendingTarget.ID)
		if m.pendingTarget.Name != "" {
			prompt += " (" + m.pendingTarget.Name + ")"
		}
		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("214")).
			Padding(0, 1).
			Render(lipgloss.JoinVertical(lipgloss.Left,
				errorStyle.Render(prompt),
				"Type the instance ID to confirm:",
				m.confirmInput.View(),
				mutedStyle.Render("enter: run · esc: cancel"),
			))
		parts = append(parts, box)
	}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) renderListHeader() string {
	running, stopped := 0, 0
	for _, inst := range m.instances {
		switch inst.State {
		case "running":
			running++
		case "stopped":
			stopped++
		}
	}
	header := headerStyle.Render(fmt.Sprintf("EC2 Instances (%d running, %d stopped)", running, stopped))
	badge := mutedStyle.Render(fmt.Sprintf("  state: %s", m.stateFilter()))
	return header + badge
}

func buildRows(instances []Instance) []datatable.Row {
	rows := make([]datatable.Row, len(instances))
	for i, inst := range instances {
		rows[i] = datatable.Row{
			inst.Name,
			inst.ID,
			stateBadge(inst.State),
			inst.Type,
			inst.PrivIP,
		}
	}
	return rows
}

// stateBadge colours the EC2 instance state. Steady-states map to green
// (running) and a muted dash (stopped); transitional states are yellow;
// the terminal terminated state is red. Empty falls through to N/A so the
// column never looks blank.
func stateBadge(state string) string {
	if state == "" {
		return mutedStyle.Render("N/A")
	}
	style := lipgloss.NewStyle()
	switch state {
	case "running":
		style = style.Foreground(lipgloss.Color("34"))
	case "stopped":
		return mutedStyle.Render(state)
	case "terminated":
		style = style.Foreground(lipgloss.Color("160"))
	case "pending", "stopping", "shutting-down":
		style = style.Foreground(lipgloss.Color("214"))
	default:
		return mutedStyle.Render(state)
	}
	return style.Render(state)
}

var (
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle = lipgloss.NewStyle().Bold(true)
)
