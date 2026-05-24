package ec2

import (
	"context"
	"encoding/gob"
	"fmt"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/loader"
	"github.com/huy-tran/aws-tui/internal/ui/statusbar"
)

const instancesCacheTTL = 60 * time.Second

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

type Model struct {
	ctx        *awspkg.Context
	table      datatable.Model
	loader     loader.Model
	filter     textinput.Model
	filterMode bool
	instances  []Instance // full list as returned by DescribeInstances
	displayed  []Instance // visible after filter; equals instances when no filter
	loading    bool
	err        error
	width      int
	height     int
	lastLoaded time.Time
	status     string
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

	return Model{
		ctx:     ctx,
		table:   t,
		loader:  loader.New(),
		filter:  ti,
		loading: true,
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

// applyFilter rebuilds m.displayed and the table rows based on the current
// filter text. Empty filter shows everything. Allocates a fresh slice on
// narrow so it never aliases m.instances - aliasing previously caused
// duplicate / corrupted rows when the filter was tightened in steps.
func (m *Model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		m.displayed = m.instances
	} else {
		out := make([]Instance, 0, len(m.instances))
		for _, inst := range m.instances {
			if instanceMatches(inst, q) {
				out = append(out, inst)
			}
		}
		m.displayed = out
	}
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
	return m.filterMode
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
	return []help.Section{{
		Title: "EC2 instances",
		Items: []help.Item{
			{Keys: "enter", Desc: "SSM start-session into selected instance"},
			{Keys: "p", Desc: "port-forward modal"},
			{Keys: "i", Desc: "instance details"},
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

func (m Model) startSSMSession(inst Instance) tea.Cmd {
	cmd := execAWS("ssm", "start-session",
		"--profile", m.ctx.Profile,
		"--region", m.ctx.Region,
		"--target", inst.ID,
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

	help := mutedStyle.Render("enter: SSM · p: port forward · i: details · r: refresh · /: filter")

	body := m.table.View()
	if len(m.displayed) == 0 {
		if m.filter.Value() != "" {
			body = mutedStyle.Render("No instances match filter.")
		} else {
			body = mutedStyle.Render("No instances in this region.")
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, filterLine, body, help)
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
	return headerStyle.Render(fmt.Sprintf("EC2 Instances (%d running, %d stopped)", running, stopped))
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
