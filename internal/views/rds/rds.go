package rds

import (
	"context"
	"encoding/gob"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	rdssdk "github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/atotto/clipboard"
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

const dbInstancesCacheTTL = 60 * time.Second

func init() { gob.Register([]DBInstance(nil)) }

type DBInstance struct {
	ID             string
	Engine         string
	EngineVersion  string
	Status         string
	Endpoint       string
	Port           int32
	Class          string
	MultiAZ        bool
	DBName         string
	MasterUser     string
	VPC            string
	SubnetGroup    string
	SecurityGroups []string
	AllocatedGB    int32
	StorageType    string
	Iops           int32
	Created        string
}

type (
	loadedMsg struct{ items []DBInstance }
	errMsg    struct{ err error }
)

type mode int

const (
	modeList mode = iota
	modeDetails
	modePortForward
)

type Model struct {
	ctx    *awspkg.Context
	mode   mode
	width  int
	height int

	instances []DBInstance
	displayed []DBInstance
	table     datatable.Model
	filter    textinput.Model
	filterMode bool

	target      DBInstance
	yankPending bool

	bastionInput  textinput.Model
	remotePortIn  textinput.Model
	localPortIn   textinput.Model
	pfFocus       int // 0 bastion, 1 remote, 2 local

	loading    bool
	loader     loader.Model
	lastLoaded time.Time
	err        error
	status     string
}

func New(ctx *awspkg.Context) Model {
	cols := []datatable.Column{
		{Title: "Identifier", Flex: true},
		{Title: "Engine"},
		{Title: "Status"},
		{Title: "Endpoint"},
	}
	t := datatable.New(cols)
	t.SetHeight(20)

	flt := textinput.New()
	flt.Placeholder = "filter by identifier / engine / endpoint / DB name"
	flt.CharLimit = 128
	flt.Prompt = "/ "

	bastion := textinput.New()
	bastion.Placeholder = "i-0123456789abcdef0"
	bastion.SetValue("i-")
	bastion.CharLimit = 64

	remote := textinput.New()
	remote.CharLimit = 6

	local := textinput.New()
	local.CharLimit = 6

	return Model{
		ctx:          ctx,
		table:        t,
		filter:       flt,
		bastionInput: bastion,
		remotePortIn: remote,
		localPortIn:  local,
		loader:       loader.New(),
		loading:      true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.loadCmd(false))
}

func (m Model) loadCmd(force bool) tea.Cmd {
	key := "rds:instances:" + m.ctx.Region
	ctx := m.ctx
	return func() tea.Msg {
		if !force {
			if cached, ok := ctx.Cache.Get(key); ok {
				if items, ok := cached.([]DBInstance); ok {
					return loadedMsg{items: items}
				}
			}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := ctx.RDS()
		var items []DBInstance
		paginator := rdssdk.NewDescribeDBInstancesPaginator(client, &rdssdk.DescribeDBInstancesInput{})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(context.Background())
			if err != nil {
				return errMsg{err: err}
			}
			for _, db := range page.DBInstances {
				items = append(items, parseInstance(db))
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		ctx.Cache.Set(key, items, dbInstancesCacheTTL)
		return loadedMsg{items: items}
	}
}

func parseInstance(d rdstypes.DBInstance) DBInstance {
	out := DBInstance{
		ID:            awssdk.ToString(d.DBInstanceIdentifier),
		Engine:        awssdk.ToString(d.Engine),
		EngineVersion: awssdk.ToString(d.EngineVersion),
		Status:        awssdk.ToString(d.DBInstanceStatus),
		Class:         awssdk.ToString(d.DBInstanceClass),
		MultiAZ:       awssdk.ToBool(d.MultiAZ),
		DBName:        awssdk.ToString(d.DBName),
		MasterUser:    awssdk.ToString(d.MasterUsername),
		AllocatedGB:   awssdk.ToInt32(d.AllocatedStorage),
		StorageType:   awssdk.ToString(d.StorageType),
		Iops:          awssdk.ToInt32(d.Iops),
	}
	if d.Endpoint != nil {
		out.Endpoint = awssdk.ToString(d.Endpoint.Address)
		out.Port = awssdk.ToInt32(d.Endpoint.Port)
	}
	if d.DBSubnetGroup != nil {
		out.VPC = awssdk.ToString(d.DBSubnetGroup.VpcId)
		out.SubnetGroup = awssdk.ToString(d.DBSubnetGroup.DBSubnetGroupName)
	}
	for _, sg := range d.VpcSecurityGroups {
		out.SecurityGroups = append(out.SecurityGroups, awssdk.ToString(sg.VpcSecurityGroupId))
	}
	if d.InstanceCreateTime != nil {
		out.Created = d.InstanceCreateTime.Local().Format("2006-01-02 15:04:05")
	}
	return out
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 4
		if h < 3 {
			h = 3
		}
		m.table.SetHeight(h)
		m.table.SetWidth(msg.Width)
		return m, nil

	case loadedMsg:
		m.loading = false
		m.instances = msg.items
		m.lastLoaded = time.Now()
		m.applyFilter()
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
		return m.updateDetailsKeys(msg)
	case modePortForward:
		return m.updatePortForwardKeys(msg)
	}
	return m.updateListKeys(msg)
}

func (m Model) updateListKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	if m.yankPending {
		m.yankPending = false
		db := m.selected()
		if db == nil {
			m.status = "no instance selected"
			return m, nil
		}
		switch msg.String() {
		case "h":
			m.status = doYank(db.Endpoint, "endpoint host")
		case "p":
			m.status = doYank(strconv.FormatInt(int64(db.Port), 10), "port")
		case "u":
			m.status = doYank(db.MasterUser, "master user")
		default:
			m.status = ""
		}
		return m, nil
	}

	switch msg.String() {
	case "/":
		m.filterMode = true
		m.filter.Focus()
		m.status = ""
		return m, textinput.Blink
	case "r":
		m.ctx.Cache.Invalidate("rds:instances:" + m.ctx.Region)
		m.loading = true
		m.err = nil
		return m, tea.Batch(m.loader.Tick(), m.loadCmd(true))
	case "y":
		m.yankPending = true
		m.status = "yank: h=endpoint host, p=port, u=master user"
		return m, nil
	case "enter":
		if db := m.selected(); db != nil {
			m.target = *db
			m.mode = modeDetails
			m.status = ""
			return m, nil
		}
	case "f":
		if db := m.selected(); db != nil {
			m.target = *db
			m.openPortForward(*db)
			return m, textinput.Blink
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m Model) updateDetailsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.yankPending {
		m.yankPending = false
		switch msg.String() {
		case "h":
			m.status = doYank(m.target.Endpoint, "endpoint host")
		case "p":
			m.status = doYank(strconv.FormatInt(int64(m.target.Port), 10), "port")
		case "u":
			m.status = doYank(m.target.MasterUser, "master user")
		default:
			m.status = ""
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.status = ""
		return m, nil
	case "f":
		m.openPortForward(m.target)
		return m, textinput.Blink
	case "y":
		m.yankPending = true
		m.status = "yank: h=endpoint host, p=port, u=master user"
		return m, nil
	}
	return m, nil
}

func (m *Model) openPortForward(db DBInstance) {
	m.mode = modePortForward
	port := strconv.FormatInt(int64(db.Port), 10)
	m.remotePortIn.SetValue(port)
	// Default local port: remote + 10000 to avoid the privileged range
	// collision; user can edit.
	m.localPortIn.SetValue(strconv.FormatInt(int64(db.Port)+10000, 10))
	m.pfFocus = 0
	m.bastionInput.Focus()
	m.remotePortIn.Blur()
	m.localPortIn.Blur()
	m.status = ""
}

func (m Model) updatePortForwardKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeDetails
		if m.target.ID == "" {
			m.mode = modeList
		}
		m.bastionInput.Blur()
		m.remotePortIn.Blur()
		m.localPortIn.Blur()
		return m, nil
	case "ctrl+s":
		cmd, err := buildPortForwardCommand(m.ctx.Profile, m.ctx.Region,
			strings.TrimSpace(m.bastionInput.Value()),
			m.target.Endpoint,
			strings.TrimSpace(m.remotePortIn.Value()),
			strings.TrimSpace(m.localPortIn.Value()))
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.status = doYank(cmd, "port-forward command")
		return m, nil
	case "tab":
		m.pfFocus = (m.pfFocus + 1) % 3
		m.applyPFFocus()
		return m, textinput.Blink
	case "shift+tab":
		m.pfFocus = (m.pfFocus + 2) % 3
		m.applyPFFocus()
		return m, textinput.Blink
	}
	var cmd tea.Cmd
	switch m.pfFocus {
	case 0:
		m.bastionInput, cmd = m.bastionInput.Update(msg)
	case 1:
		m.remotePortIn, cmd = m.remotePortIn.Update(msg)
	case 2:
		m.localPortIn, cmd = m.localPortIn.Update(msg)
	}
	return m, cmd
}

func (m *Model) applyPFFocus() {
	m.bastionInput.Blur()
	m.remotePortIn.Blur()
	m.localPortIn.Blur()
	switch m.pfFocus {
	case 0:
		m.bastionInput.Focus()
	case 1:
		m.remotePortIn.Focus()
	case 2:
		m.localPortIn.Focus()
	}
}

func buildPortForwardCommand(profile, region, bastion, host, remotePort, localPort string) (string, error) {
	if bastion == "" || !strings.HasPrefix(bastion, "i-") {
		return "", fmt.Errorf("bastion must start with i-")
	}
	if host == "" {
		return "", fmt.Errorf("RDS endpoint missing")
	}
	if remotePort == "" {
		return "", fmt.Errorf("remote port missing")
	}
	if localPort == "" {
		return "", fmt.Errorf("local port missing")
	}
	params := fmt.Sprintf(`{"host":["%s"],"portNumber":["%s"],"localPortNumber":["%s"]}`,
		host, remotePort, localPort)
	return fmt.Sprintf(
		"aws ssm start-session --profile %s --region %s --target %s --document-name AWS-StartPortForwardingSessionToRemoteHost --parameters '%s'",
		profile, region, bastion, params), nil
}

func (m Model) CapturingInput() bool {
	if m.mode == modeList && (m.filterMode || m.yankPending) {
		return true
	}
	return m.mode == modePortForward
}

func (m Model) InSubnav() bool { return m.mode != modeList }

func (m Model) BookmarkCurrent() (string, string, bool) {
	db := m.selected()
	if db == nil {
		return "", "", false
	}
	return db.ID, db.ID, true
}

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
	switch m.mode {
	case modeDetails:
		return []help.Section{{
			Title: "RDS · details",
			Items: []help.Item{
				{Keys: "f", Desc: "build port-forward command"},
				{Keys: "y", Desc: "yank menu: h=endpoint, p=port, u=master user"},
				{Keys: "esc", Desc: "back to list"},
			},
		}}
	case modePortForward:
		return []help.Section{{
			Title: "RDS · port-forward",
			Items: []help.Item{
				{Keys: "tab / S-tab", Desc: "cycle fields"},
				{Keys: "ctrl+s", Desc: "yank built command (does not run)"},
				{Keys: "esc", Desc: "cancel"},
			},
		}}
	}
	if m.filterMode {
		return []help.Section{{
			Title: "RDS · filter",
			Items: []help.Item{
				{Keys: "type", Desc: "match identifier / engine / endpoint / DB name"},
				{Keys: "enter", Desc: "apply and exit filter"},
				{Keys: "esc", Desc: "clear and exit filter"},
			},
		}}
	}
	return []help.Section{{
		Title: "RDS instances",
		Items: []help.Item{
			{Keys: "enter", Desc: "details"},
			{Keys: "f", Desc: "build port-forward command"},
			{Keys: "y", Desc: "yank menu: h=endpoint, p=port, u=master user"},
			{Keys: "/", Desc: "filter"},
			{Keys: "r", Desc: "refresh"},
		},
	}}
}

func (m *Model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		m.displayed = m.instances
	} else {
		out := make([]DBInstance, 0, len(m.instances))
		for _, db := range m.instances {
			if matchesFilter(db, q) {
				out = append(out, db)
			}
		}
		m.displayed = out
	}
	m.table.SetRows(buildRows(m.displayed))
}

func matchesFilter(db DBInstance, q string) bool {
	return strings.Contains(strings.ToLower(db.ID), q) ||
		strings.Contains(strings.ToLower(db.Engine), q) ||
		strings.Contains(strings.ToLower(db.Endpoint), q) ||
		strings.Contains(strings.ToLower(db.DBName), q)
}

func (m Model) selected() *DBInstance {
	if m.loading || len(m.displayed) == 0 {
		return nil
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.displayed) {
		return nil
	}
	return &m.displayed[idx]
}

func buildRows(items []DBInstance) []datatable.Row {
	rows := make([]datatable.Row, len(items))
	for i, db := range items {
		rows[i] = datatable.Row{
			db.ID,
			db.Engine + " " + db.EngineVersion,
			statusBadge(db.Status),
			db.Endpoint,
		}
	}
	return rows
}

// statusBadge colours the RDS instance status. available is the steady
// green state; transitional / maintenance states are yellow; explicit
// failure modes are red; stopped is muted.
func statusBadge(status string) string {
	if status == "" {
		return mutedStyle.Render("N/A")
	}
	style := lipgloss.NewStyle()
	switch status {
	case "available":
		style = style.Foreground(lipgloss.Color("34"))
	case "stopped":
		return mutedStyle.Render(status)
	case "failed", "incompatible-network", "incompatible-restore", "incompatible-credentials", "inaccessible-encryption-credentials":
		style = style.Foreground(lipgloss.Color("160"))
	case "creating", "deleting", "backing-up", "modifying", "rebooting",
		"renaming", "resetting-master-credentials", "storage-optimization",
		"starting", "stopping", "upgrading", "maintenance":
		style = style.Foreground(lipgloss.Color("214"))
	default:
		return mutedStyle.Render(status)
	}
	return style.Render(status)
}

func doYank(value, label string) string {
	if value == "" {
		return "nothing to copy for " + label
	}
	if err := clipboard.WriteAll(value); err != nil {
		return "clipboard error: " + err.Error()
	}
	return "copied " + label
}

// --- view ----------------------------------------------------------------

func (m Model) View() string {
	if m.err != nil {
		hint := "Press r to retry."
		if _, ok := m.err.(*awspkg.SSOExpiredError); ok {
			hint = "Run `aws sso login --profile " + m.ctx.Profile + "` then press r."
		}
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\n%s", m.err, hint))
	}
	switch m.mode {
	case modeDetails:
		return m.viewDetails()
	case modePortForward:
		return m.viewPortForward()
	}
	return m.viewList()
}

func (m Model) viewList() string {
	if m.loading {
		return m.loader.Render("loading instances...")
	}
	if len(m.instances) == 0 {
		return mutedStyle.Render("No RDS instances in this region.")
	}
	header := headerStyle.Render(fmt.Sprintf("RDS Instances (%d)", len(m.instances)))
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}
	body := m.table.View()
	if len(m.displayed) == 0 && m.filter.Value() != "" {
		body = mutedStyle.Render("No instances match filter.")
	}
	help := mutedStyle.Render("enter: details · f: port-forward · y: yank · /: filter · r: refresh")
	parts := []string{header, filterLine, body, help}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) viewDetails() string {
	d := m.target
	title := headerStyle.Render(d.ID)
	body := strings.Join([]string{
		field("Engine", d.Engine+" "+d.EngineVersion),
		field("Class", d.Class),
		field("Status", statusBadge(d.Status)),
		field("Multi-AZ", boolLabel(d.MultiAZ)),
		field("Endpoint", emptyDash(d.Endpoint)),
		field("Port", strconv.FormatInt(int64(d.Port), 10)),
		field("DB name", emptyDash(d.DBName)),
		field("Master user", emptyDash(d.MasterUser)),
		field("VPC", emptyDash(d.VPC)),
		field("Subnet grp", emptyDash(d.SubnetGroup)),
		field("Security", strings.Join(d.SecurityGroups, ", ")),
		field("Storage", storageLabel(d)),
		field("Created", emptyDash(d.Created)),
	}, "\n")
	help := mutedStyle.Render("f: port-forward · y: yank menu · esc: back")
	parts := []string{title, "", body, "", help}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) viewPortForward() string {
	title := headerStyle.Render("Port-forward to " + m.target.ID)
	parts := []string{
		title, "",
		mutedStyle.Render(focusLabel("Bastion instance", m.pfFocus == 0)),
		m.bastionInput.View(),
		mutedStyle.Render(focusLabel("Remote port", m.pfFocus == 1)),
		m.remotePortIn.View(),
		mutedStyle.Render(focusLabel("Local port", m.pfFocus == 2)),
		m.localPortIn.View(),
		"",
		mutedStyle.Render("ctrl+s: yank command (does not run) · tab: cycle · esc: cancel"),
	}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func field(label, value string) string {
	return fmt.Sprintf("  %-14s %s", label+":", value)
}

func focusLabel(label string, focused bool) string {
	if focused {
		return "▶ " + label
	}
	return "  " + label
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func boolLabel(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func storageLabel(d DBInstance) string {
	if d.AllocatedGB == 0 {
		return "-"
	}
	out := fmt.Sprintf("%d GB", d.AllocatedGB)
	if d.StorageType != "" {
		out += " " + d.StorageType
	}
	if d.Iops > 0 {
		out += fmt.Sprintf(" (iops %d)", d.Iops)
	}
	return out
}

var (
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle = lipgloss.NewStyle().Bold(true)
)
