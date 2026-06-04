package cloudfront

import (
	"context"
	"encoding/gob"
	"fmt"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	cf "github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
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

const distributionsCacheTTL = 60 * time.Second

func init() {
	gob.Register([]Distribution(nil))
	gob.Register([]Invalidation(nil))
}
const invalidationsCacheTTL = 30 * time.Second
const invalidationPollInterval = 5 * time.Second

type Distribution struct {
	ID         string
	DomainName string
	Origin     string
	Status     string
	Enabled    bool
}

type Invalidation struct {
	ID      string
	Status  string
	Created string
	Paths   []string
}

type (
	distributionsLoadedMsg struct{ items []Distribution }
	invalidationsLoadedMsg struct {
		distID string
		items  []Invalidation
	}
	invalidationCreatedMsg struct{ distID, id string }
	tickMsg                struct{}
	errMsg                 struct{ err error }
)

type mode int

const (
	modeList mode = iota
	modeInvalidatePaths
	modeInvalidateConfirm
	modeInvalidateConfirmWildcard
	modeInvalidations
)

type Model struct {
	ctx    *awspkg.Context
	mode   mode
	width  int
	height int

	// list state
	table       datatable.Model
	distros     []Distribution
	distrosFilt []Distribution
	filter      textinput.Model
	filterMode  bool
	loading     bool
	err         error
	status      string

	// invalidation modal state
	paths     textarea.Model
	wildcard  textinput.Model
	target    Distribution
	pending   []string

	// invalidations history state
	histTable    datatable.Model
	hist         []Invalidation
	histLoading  bool
	histPolling  bool

	loader     loader.Model
	lastLoaded time.Time
}

func New(ctx *awspkg.Context) Model {
	cols := []datatable.Column{
		{Title: "ID"},
		{Title: "Domain", Flex: true},
		{Title: "Origin"},
		{Title: "Status"},
	}
	t := datatable.New(cols)
	t.SetHeight(15)

	ta := textarea.New()
	ta.Placeholder = "/*"
	ta.SetHeight(6)

	ti := textinput.New()
	ti.Placeholder = "type INVALIDATE to confirm"
	ti.CharLimit = 16

	histCols := []datatable.Column{
		{Title: "ID"},
		{Title: "Status"},
		{Title: "Created", SortAs: datatable.SortTime},
		{Title: "Paths", Flex: true},
	}
	hist := datatable.New(histCols)
	hist.SetHeight(15)

	flt := textinput.New()
	flt.Placeholder = "filter by id / domain / origin"
	flt.CharLimit = 64
	flt.Prompt = "/ "

	return Model{
		ctx:       ctx,
		table:     t,
		paths:     ta,
		wildcard:  ti,
		histTable: hist,
		filter:    flt,
		loader:    loader.New(),
		loading:   true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.loadCmd(false))
}

func (m Model) loadCmd(force bool) tea.Cmd {
	const key = "cloudfront:distributions"
	ctx := m.ctx
	return func() tea.Msg {
		if !force {
			if cached, ok := ctx.Cache.Get(key); ok {
				if items, ok := cached.([]Distribution); ok {
					return distributionsLoadedMsg{items: items}
				}
			}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := ctx.CloudFront()
		var items []Distribution
		paginator := cf.NewListDistributionsPaginator(client, &cf.ListDistributionsInput{})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(context.Background())
			if err != nil {
				return errMsg{err: err}
			}
			if page.DistributionList == nil {
				continue
			}
			for _, d := range page.DistributionList.Items {
				items = append(items, Distribution{
					ID:         awssdk.ToString(d.Id),
					DomainName: awssdk.ToString(d.DomainName),
					Origin:     firstOrigin(d.Origins),
					Status:     awssdk.ToString(d.Status),
					Enabled:    awssdk.ToBool(d.Enabled),
				})
			}
		}
		ctx.Cache.Set(key, items, distributionsCacheTTL)
		return distributionsLoadedMsg{items: items}
	}
}

func firstOrigin(origins *cftypes.Origins) string {
	if origins == nil || len(origins.Items) == 0 {
		return ""
	}
	return awssdk.ToString(origins.Items[0].DomainName)
}

func (m Model) loadInvalidationsCmd(distID string, force bool) tea.Cmd {
	key := "cloudfront:invalidations:" + distID
	ctx := m.ctx
	return func() tea.Msg {
		if !force {
			if cached, ok := ctx.Cache.Get(key); ok {
				if items, ok := cached.([]Invalidation); ok {
					return invalidationsLoadedMsg{distID: distID, items: items}
				}
			}
		}
		client := ctx.CloudFront()
		out, err := client.ListInvalidations(context.Background(), &cf.ListInvalidationsInput{
			DistributionId: awssdk.String(distID),
		})
		if err != nil {
			return errMsg{err: err}
		}
		var items []Invalidation
		if out.InvalidationList != nil {
			for _, inv := range out.InvalidationList.Items {
				it := Invalidation{
					ID:     awssdk.ToString(inv.Id),
					Status: awssdk.ToString(inv.Status),
				}
				if inv.CreateTime != nil {
					it.Created = inv.CreateTime.Local().Format("2006-01-02 15:04:05")
				}
				items = append(items, it)
			}
		}
		ctx.Cache.Set(key, items, invalidationsCacheTTL)
		return invalidationsLoadedMsg{distID: distID, items: items}
	}
}

func (m Model) createInvalidationCmd(distID string, paths []string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		action := audit.Action{
			Profile: ctx.Profile,
			Region:  ctx.Region,
			Action:  "cloudfront:CreateInvalidation",
			Target:  distID,
			Payload: map[string]any{"paths": paths},
		}
		if audit.IsDryRun() {
			audit.Log(action, true, "")
			ctx.Cache.Invalidate("cloudfront:invalidations:" + distID)
			return invalidationCreatedMsg{distID: distID, id: "dry-run"}
		}
		client := ctx.CloudFront()
		ref := fmt.Sprintf("aws-tui-%d", time.Now().Unix())
		out, err := client.CreateInvalidation(context.Background(), &cf.CreateInvalidationInput{
			DistributionId: awssdk.String(distID),
			InvalidationBatch: &cftypes.InvalidationBatch{
				CallerReference: awssdk.String(ref),
				Paths: &cftypes.Paths{
					Quantity: awssdk.Int32(int32(len(paths))),
					Items:    paths,
				},
			},
		})
		if err != nil {
			return errMsg{err: err}
		}
		id := awssdk.ToString(out.Invalidation.Id)
		audit.Log(action, false, id)
		ctx.Cache.Invalidate("cloudfront:invalidations:" + distID)
		return invalidationCreatedMsg{distID: distID, id: id}
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
		m.table.SetHeight(h)
		m.table.SetWidth(msg.Width)
		m.histTable.SetHeight(h)
		m.histTable.SetWidth(msg.Width)
		return m, nil

	case distributionsLoadedMsg:
		m.loading = false
		m.distros = msg.items
		m.lastLoaded = time.Now()
		m.applyDistroFilter()
		return m, nil

	case invalidationsLoadedMsg:
		m.histLoading = false
		m.hist = msg.items
		m.histTable.SetRows(buildHistRows(m.hist))
		if hasInProgress(m.hist) && !m.histPolling {
			m.histPolling = true
			return m, tickEvery(invalidationPollInterval)
		}
		if !hasInProgress(m.hist) {
			m.histPolling = false
		}
		return m, nil

	case invalidationCreatedMsg:
		if msg.id == "dry-run" {
			m.status = fmt.Sprintf("dry-run: invalidate on %s", msg.distID)
		} else {
			m.status = fmt.Sprintf("Created invalidation %s on %s", msg.id, msg.distID)
		}
		m.mode = modeInvalidations
		m.target.ID = msg.distID
		m.histLoading = true
		return m, tea.Batch(m.loader.Tick(), m.loadInvalidationsCmd(msg.distID, true))

	case tickMsg:
		if m.mode == modeInvalidations && m.histPolling {
			return m, tea.Batch(
				m.loadInvalidationsCmd(m.target.ID, true),
				tickEvery(invalidationPollInterval),
			)
		}
		return m, nil

	case errMsg:
		m.loading = false
		m.histLoading = false
		m.err = msg.err
		return m, nil

	case spinner.TickMsg:
		if !m.loading && !m.histLoading {
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
	case modeInvalidatePaths:
		switch msg.String() {
		case "esc":
			m.mode = modeList
			return m, nil
		case "ctrl+s", "alt+enter":
			return m.confirmInvalidate()
		}
		var cmd tea.Cmd
		m.paths, cmd = m.paths.Update(msg)
		return m, cmd

	case modeInvalidateConfirm:
		switch msg.String() {
		case "y":
			m.mode = modeList
			cmd := m.createInvalidationCmd(m.target.ID, m.pending)
			m.pending = nil
			return m, cmd
		case "n", "esc":
			m.mode = modeInvalidatePaths
			return m, nil
		}
		return m, nil

	case modeInvalidateConfirmWildcard:
		switch msg.String() {
		case "esc":
			m.mode = modeInvalidatePaths
			return m, nil
		case "enter":
			if m.wildcard.Value() == "INVALIDATE" {
				m.mode = modeList
				cmd := m.createInvalidationCmd(m.target.ID, m.pending)
				m.pending = nil
				m.wildcard.SetValue("")
				return m, cmd
			}
			m.status = "Type INVALIDATE exactly to confirm full cache flush"
			return m, nil
		}
		var cmd tea.Cmd
		m.wildcard, cmd = m.wildcard.Update(msg)
		return m, cmd

	case modeInvalidations:
		switch msg.String() {
		case "esc":
			m.mode = modeList
			m.histPolling = false
			return m, nil
		case "r":
			m.ctx.Cache.Invalidate("cloudfront:invalidations:" + m.target.ID)
			m.histLoading = true
			return m, tea.Batch(m.loader.Tick(), m.loadInvalidationsCmd(m.target.ID, true))
		}
		var cmd tea.Cmd
		m.histTable, cmd = m.histTable.Update(msg)
		return m, cmd

	default: // modeList
		if m.filterMode {
			switch msg.String() {
			case "esc":
				m.filterMode = false
				m.filter.SetValue("")
				m.filter.Blur()
				m.applyDistroFilter()
				return m, nil
			case "enter":
				m.filterMode = false
				m.filter.Blur()
				return m, nil
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.applyDistroFilter()
			return m, cmd
		}
		switch msg.String() {
		case "/":
			m.filterMode = true
			m.filter.Focus()
			return m, textinput.Blink
		case "r":
			m.ctx.Cache.Invalidate("cloudfront:distributions")
			m.loading = true
			m.err = nil
			m.status = ""
			return m, tea.Batch(m.loader.Tick(), m.loadCmd(true))
		case "i":
			if d := m.selectedDistro(); d != nil {
				m.target = *d
				m.mode = modeInvalidatePaths
				m.paths.SetValue("")
				m.paths.Focus()
				return m, textarea.Blink
			}
		case "v":
			if d := m.selectedDistro(); d != nil {
				m.target = *d
				m.mode = modeInvalidations
				m.histLoading = true
				return m, tea.Batch(m.loader.Tick(), m.loadInvalidationsCmd(d.ID, false))
			}
		}
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	}
}

func (m *Model) applyDistroFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		m.distrosFilt = m.distros
	} else {
		out := make([]Distribution, 0, len(m.distros))
		for _, d := range m.distros {
			if strings.Contains(strings.ToLower(d.ID), q) ||
				strings.Contains(strings.ToLower(d.DomainName), q) ||
				strings.Contains(strings.ToLower(d.Origin), q) {
				out = append(out, d)
			}
		}
		m.distrosFilt = out
	}
	m.table.SetRows(buildDistroRows(m.distrosFilt))
}

func (m Model) CapturingInput() bool {
	if m.mode == modeList && m.filterMode {
		return true
	}
	return m.mode == modeInvalidatePaths || m.mode == modeInvalidateConfirmWildcard
}

func (m Model) InSubnav() bool {
	return m.mode != modeList
}

func (m Model) BookmarkCurrent() (string, string, bool) {
	d := m.selectedDistro()
	if d == nil {
		return "", "", false
	}
	label := d.DomainName
	if label == "" {
		label = d.ID
	} else {
		label = fmt.Sprintf("%s (%s)", label, d.ID)
	}
	return label, d.ID, true
}

func (m Model) SetFilterQuery(q string) tea.Model {
	m.filter.SetValue(q)
	m.applyDistroFilter()
	return m
}

func (m Model) StatusFooter() statusbar.Snapshot {
	items := len(m.distrosFilt)
	if m.mode == modeInvalidations {
		items = len(m.hist)
	}
	return statusbar.Snapshot{
		Items:      items,
		LastLoaded: m.lastLoaded,
		Message:    m.status,
	}
}

func (m Model) HelpItems() []help.Section {
	switch m.mode {
	case modeInvalidatePaths:
		return []help.Section{{
			Title: "CloudFront · invalidation paths",
			Items: []help.Item{
				{Keys: "type", Desc: "enter paths, one per line; /* = full cache flush"},
				{Keys: "ctrl+s / alt+enter", Desc: "submit"},
				{Keys: "esc", Desc: "cancel"},
			},
		}}
	case modeInvalidateConfirm, modeInvalidateConfirmWildcard:
		return []help.Section{{
			Title: "CloudFront · confirm invalidation",
			Items: []help.Item{
				{Keys: "y / enter", Desc: "confirm (wildcard requires typing INVALIDATE)"},
				{Keys: "n / esc", Desc: "back to paths"},
			},
		}}
	case modeInvalidations:
		return []help.Section{{
			Title: "CloudFront · invalidations history",
			Items: []help.Item{
				{Keys: "r", Desc: "refresh"},
				{Keys: "esc", Desc: "back to distributions"},
			},
		}}
	}
	if m.filterMode {
		return []help.Section{{
			Title: "CloudFront · filter",
			Items: []help.Item{
				{Keys: "type", Desc: "match id / domain / origin"},
				{Keys: "enter", Desc: "apply and exit filter"},
				{Keys: "esc", Desc: "clear and exit filter"},
			},
		}}
	}
	return []help.Section{{
		Title: "CloudFront distributions",
		Items: []help.Item{
			{Keys: "i", Desc: "create invalidation"},
			{Keys: "v", Desc: "view invalidations history"},
			{Keys: "/", Desc: "filter"},
			{Keys: "r", Desc: "refresh"},
			{Keys: "↑/↓ j/k", Desc: "move cursor"},
		},
	}}
}

func (m Model) confirmInvalidate() (tea.Model, tea.Cmd) {
	paths := parsePaths(m.paths.Value())
	if len(paths) == 0 {
		m.status = "Enter at least one path"
		return m, nil
	}
	m.pending = paths
	for _, p := range paths {
		if p == "/*" {
			m.mode = modeInvalidateConfirmWildcard
			m.wildcard.SetValue("")
			m.wildcard.Focus()
			return m, textinput.Blink
		}
	}
	m.mode = modeInvalidateConfirm
	return m, nil
}

func parsePaths(input string) []string {
	var out []string
	for _, l := range strings.Split(input, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (m Model) selectedDistro() *Distribution {
	if m.loading || len(m.distrosFilt) == 0 {
		return nil
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.distrosFilt) {
		return nil
	}
	return &m.distrosFilt[idx]
}

func hasInProgress(items []Invalidation) bool {
	for _, it := range items {
		if it.Status == "InProgress" {
			return true
		}
	}
	return false
}

func (m Model) View() string {
	if m.err != nil {
		hint := "Press r to retry."
		if _, ok := m.err.(*awspkg.SSOExpiredError); ok {
			hint = "Run `aws sso login --profile " + m.ctx.Profile + "` then press r."
		}
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\n%s", m.err, hint))
	}

	switch m.mode {
	case modeInvalidatePaths:
		return m.viewInvalidate()
	case modeInvalidateConfirm:
		return m.viewConfirm()
	case modeInvalidateConfirmWildcard:
		return m.viewConfirmWildcard()
	case modeInvalidations:
		return m.viewInvalidations()
	}

	if m.loading {
		return m.loader.Render("loading distributions...")
	}
	if len(m.distros) == 0 {
		return mutedStyle.Render("No CloudFront distributions in this account.")
	}
	header := headerStyle.Render(fmt.Sprintf("CloudFront Distributions (%d)", len(m.distros)))
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}
	body := m.table.View()
	if len(m.distrosFilt) == 0 && m.filter.Value() != "" {
		body = mutedStyle.Render("No distributions match filter.")
	}
	help := mutedStyle.Render("enter: open · i: invalidate · v: invalidations · /: filter · r: refresh")
	parts := []string{header, filterLine, "", body, "", help}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) viewInvalidate() string {
	title := headerStyle.Render(fmt.Sprintf("Create Invalidation: %s", m.target.ID))
	domain := mutedStyle.Render(m.target.DomainName)
	prompt := "Enter paths (one per line). Use /* for full cache flush."
	help := mutedStyle.Render("ctrl+s or alt+enter to submit · esc to cancel")
	return lipgloss.JoinVertical(lipgloss.Left, title, domain, "", prompt, "", m.paths.View(), "", help)
}

func (m Model) viewConfirm() string {
	title := headerStyle.Render("Confirm Invalidation")
	info := fmt.Sprintf("Distribution: %s\nDomain: %s\nPaths (%d):\n%s",
		m.target.ID, m.target.DomainName, len(m.pending), bulletList(m.pending))
	help := mutedStyle.Render("y to confirm · n or esc to cancel")
	return lipgloss.JoinVertical(lipgloss.Left, title, "", info, "", help)
}

func (m Model) viewConfirmWildcard() string {
	title := headerStyle.Render("CONFIRM FULL CACHE FLUSH")
	warning := errorStyle.Render("/* invalidates ALL cached content on " + m.target.DomainName + ".")
	prompt := "Type INVALIDATE to confirm:"
	help := mutedStyle.Render("enter to submit · esc to cancel")
	status := ""
	if m.status != "" {
		status = errorStyle.Render(m.status)
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, "", warning, "", prompt, m.wildcard.View(), "", status, "", help)
}

func (m Model) viewInvalidations() string {
	title := headerStyle.Render(fmt.Sprintf("Invalidations: %s", m.target.ID))
	pollNote := ""
	if m.histPolling {
		pollNote = mutedStyle.Render("auto-refresh every 5s while InProgress")
	}
	if m.histLoading {
		return lipgloss.JoinVertical(lipgloss.Left, title, "", m.loader.Render("loading invalidations..."))
	}
	help := mutedStyle.Render("r: refresh · esc: back")
	return lipgloss.JoinVertical(lipgloss.Left, title, pollNote, "", m.histTable.View(), "", help)
}

func buildDistroRows(items []Distribution) []datatable.Row {
	rows := make([]datatable.Row, len(items))
	for i, d := range items {
		rows[i] = datatable.Row{d.ID, d.DomainName, d.Origin, statusBadge(d.Status)}
	}
	return rows
}

func buildHistRows(items []Invalidation) []datatable.Row {
	rows := make([]datatable.Row, len(items))
	for i, it := range items {
		paths := strings.Join(it.Paths, " ")
		if paths == "" {
			paths = "-"
		}
		rows[i] = datatable.Row{it.ID, statusBadge(it.Status), it.Created, paths}
	}
	return rows
}

// statusBadge colours CloudFront propagation / invalidation status. The
// CloudFront API uses "Deployed" + "InProgress" for distributions and
// "Completed" + "InProgress" + "Failed" for invalidations; both surfaces
// route through here so a terminal-green steady state pops next to an
// amber in-flight one.
func statusBadge(status string) string {
	if status == "" {
		return mutedStyle.Render("N/A")
	}
	style := lipgloss.NewStyle()
	switch status {
	case "Deployed", "Completed":
		style = style.Foreground(lipgloss.Color("34"))
	case "InProgress":
		style = style.Foreground(lipgloss.Color("214"))
	case "Failed":
		style = style.Foreground(lipgloss.Color("160"))
	default:
		return mutedStyle.Render(status)
	}
	return style.Render(status)
}

func bulletList(items []string) string {
	var sb strings.Builder
	for _, it := range items {
		sb.WriteString("  • ")
		sb.WriteString(it)
		sb.WriteString("\n")
	}
	return sb.String()
}

var (
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle = lipgloss.NewStyle().Bold(true)
)
