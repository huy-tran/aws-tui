package securityhub

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	sh "github.com/aws/aws-sdk-go-v2/service/securityhub"
	shtypes "github.com/aws/aws-sdk-go-v2/service/securityhub/types"
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

const (
	insightsCacheTTL = 10 * time.Minute
	findingsCacheTTL = 60 * time.Second
	maxFindingsFetch = 500
)

func init() { gob.Register([]Insight(nil)) }

type Insight struct {
	ARN     string
	Name    string
	GroupBy string // friendly label
	RawAttr string // raw GroupByAttribute string from SDK; used to build filters
	Owner   string // "AWS" or "You"
}

type InsightResult struct {
	GroupValue string
	Count      int64
}

type FindingResource struct {
	ARN  string
	Type string
}

type Remediation struct {
	Text string
	URL  string
}

type Finding struct {
	ID          string
	Title       string
	Description string
	Severity    string // CRITICAL / HIGH / MEDIUM / LOW / INFORMATIONAL
	Workflow    string
	RecordState string // ACTIVE / ARCHIVED
	ProductName string
	AccountID   string
	Region      string
	UpdatedAt   time.Time
	Resources   []FindingResource
	Compliance  string
	Standards   []string
	Remediation Remediation
}

type (
	insightsLoadedMsg       struct{ items []Insight }
	insightResultsLoadedMsg struct {
		insight Insight
		groups  []InsightResult
	}
	findingsLoadedMsg struct {
		scope    string
		items    []Finding
		capped   bool
	}
	errMsg struct{ err error }
)

type mode int

const (
	modeInsights mode = iota
	modeInsightResults
	modeFindings
	modeFindingDetail
)

type sevFilter struct {
	critical, high, medium, low, informational bool
}

func defaultSevFilter() sevFilter {
	return sevFilter{critical: true, high: true, medium: true, low: true, informational: true}
}

func (s sevFilter) accepts(sev string) bool {
	switch sev {
	case "CRITICAL":
		return s.critical
	case "HIGH":
		return s.high
	case "MEDIUM":
		return s.medium
	case "LOW":
		return s.low
	case "INFORMATIONAL":
		return s.informational
	}
	return true
}

type Model struct {
	ctx    *awspkg.Context
	mode   mode
	width  int
	height int

	insights     []Insight
	insightsFilt []Insight
	insightTable datatable.Model
	filter       textinput.Model
	filterMode   bool
	loading      bool
	err          error
	status       string

	targetInsight Insight
	results       []InsightResult
	resultTable   datatable.Model

	findingsScope string
	findingsCapped bool
	findings      []Finding
	findingsFilt  []Finding
	findingsTable datatable.Model
	sev           sevFilter
	showSuppress  bool

	targetFinding Finding
	yankPending   bool

	loader     loader.Model
	lastLoaded time.Time
}

func New(ctx *awspkg.Context) Model {
	insightCols := []datatable.Column{
		{Title: "Name", Flex: true},
		{Title: "Group By"},
		{Title: "Source"},
	}
	insightT := datatable.New(insightCols)
	insightT.SetHeight(20)

	resultCols := []datatable.Column{
		{Title: "Group value", Flex: true},
		{Title: "Count", SortAs: datatable.SortNumeric},
	}
	resultT := datatable.New(resultCols)
	resultT.SetHeight(20)

	findingCols := []datatable.Column{
		{Title: "Sev"},
		{Title: "Title", Flex: true},
		{Title: "Product"},
		{Title: "Age"},
	}
	findingT := datatable.New(findingCols)
	findingT.SetHeight(20)

	flt := textinput.New()
	flt.Placeholder = "filter by insight name"
	flt.CharLimit = 256
	flt.Prompt = "/ "

	return Model{
		ctx:           ctx,
		insightTable:  insightT,
		resultTable:   resultT,
		findingsTable: findingT,
		filter:        flt,
		sev:           defaultSevFilter(),
		loader:        loader.New(),
		loading:       true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loader.Tick(), m.loadInsightsCmd(false))
}

// --- AWS calls ------------------------------------------------------------

func (m Model) loadInsightsCmd(force bool) tea.Cmd {
	key := "sh:insights:" + m.ctx.Region
	ctx := m.ctx
	return func() tea.Msg {
		if !force {
			if cached, ok := ctx.Cache.Get(key); ok {
				if items, ok := cached.([]Insight); ok {
					return insightsLoadedMsg{items: items}
				}
			}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return errMsg{err: err}
		}
		client := ctx.SecurityHub()
		var items []Insight
		paginator := sh.NewGetInsightsPaginator(client, &sh.GetInsightsInput{
			MaxResults: awssdk.Int32(100),
		})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(context.Background())
			if err != nil {
				return errMsg{err: wrapAccessErr(err)}
			}
			for _, in := range page.Insights {
				raw := awssdk.ToString(in.GroupByAttribute)
				items = append(items, Insight{
					ARN:     awssdk.ToString(in.InsightArn),
					Name:    awssdk.ToString(in.Name),
					GroupBy: humanGroupBy(raw),
					RawAttr: raw,
					Owner:   insightOwner(awssdk.ToString(in.InsightArn)),
				})
			}
		}
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Owner != items[j].Owner {
				return items[i].Owner == "You"
			}
			return items[i].Name < items[j].Name
		})
		ctx.Cache.Set(key, items, insightsCacheTTL)
		return insightsLoadedMsg{items: items}
	}
}

func (m Model) loadInsightResultsCmd(insight Insight) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		client := ctx.SecurityHub()
		out, err := client.GetInsightResults(context.Background(), &sh.GetInsightResultsInput{
			InsightArn: awssdk.String(insight.ARN),
		})
		if err != nil {
			return errMsg{err: wrapAccessErr(err)}
		}
		var results []InsightResult
		if out.InsightResults != nil {
			for _, r := range out.InsightResults.ResultValues {
				results = append(results, InsightResult{
					GroupValue: awssdk.ToString(r.GroupByAttributeValue),
					Count:      int64(awssdk.ToInt32(r.Count)),
				})
			}
		}
		sort.Slice(results, func(i, j int) bool { return results[i].Count > results[j].Count })
		if len(results) > 50 {
			results = results[:50]
		}
		return insightResultsLoadedMsg{insight: insight, groups: results}
	}
}

func (m Model) loadFindingsCmd(filters shtypes.AwsSecurityFindingFilters, scope string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		client := ctx.SecurityHub()
		var items []Finding
		capped := false
		paginator := sh.NewGetFindingsPaginator(client, &sh.GetFindingsInput{
			Filters:    &filters,
			MaxResults: awssdk.Int32(100),
		})
		for paginator.HasMorePages() {
			if len(items) >= maxFindingsFetch {
				capped = true
				break
			}
			page, err := paginator.NextPage(context.Background())
			if err != nil {
				return errMsg{err: wrapAccessErr(err)}
			}
			for _, f := range page.Findings {
				items = append(items, toFinding(f))
			}
		}
		sort.Slice(items, func(i, j int) bool {
			ri, rj := severityRank(items[i].Severity), severityRank(items[j].Severity)
			if ri != rj {
				return ri > rj
			}
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		})
		return findingsLoadedMsg{scope: scope, items: items, capped: capped}
	}
}

// --- Update ---------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 6
		if h < 3 {
			h = 3
		}
		m.insightTable.SetHeight(h)
		m.insightTable.SetWidth(msg.Width)
		m.resultTable.SetHeight(h)
		m.resultTable.SetWidth(msg.Width)
		m.findingsTable.SetHeight(h)
		m.findingsTable.SetWidth(msg.Width)
		return m, nil

	case insightsLoadedMsg:
		m.loading = false
		m.insights = msg.items
		m.lastLoaded = time.Now()
		m.applyInsightFilter()
		return m, nil

	case insightResultsLoadedMsg:
		m.loading = false
		m.targetInsight = msg.insight
		m.results = msg.groups
		m.lastLoaded = time.Now()
		m.resultTable.SetRows(buildResultRows(m.results))
		return m, nil

	case findingsLoadedMsg:
		m.loading = false
		m.findingsScope = msg.scope
		m.findings = msg.items
		m.findingsCapped = msg.capped
		m.lastLoaded = time.Now()
		m.applyFindingFilter()
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
	case modeInsightResults:
		return m.updateResultKeys(msg)
	case modeFindings:
		return m.updateFindingKeys(msg)
	case modeFindingDetail:
		return m.updateDetailKeys(msg)
	}
	return m.updateInsightKeys(msg)
}

// --- insights ------------------------------------------------------------

func (m Model) updateInsightKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filterMode {
		switch msg.String() {
		case "esc":
			m.filterMode = false
			m.filter.SetValue("")
			m.filter.Blur()
			m.applyInsightFilter()
			return m, nil
		case "enter":
			m.filterMode = false
			m.filter.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.applyInsightFilter()
		return m, cmd
	}
	switch msg.String() {
	case "/":
		m.filterMode = true
		m.filter.Focus()
		m.status = ""
		return m, textinput.Blink
	case "r":
		m.ctx.Cache.Invalidate("sh:insights:" + m.ctx.Region)
		m.loading = true
		m.err = nil
		return m, tea.Batch(m.loader.Tick(), m.loadInsightsCmd(true))
	case "a":
		// All active findings, no insight filter.
		m.loading = true
		m.findingsScope = "All active findings"
		m.mode = modeFindings
		return m, tea.Batch(m.loader.Tick(), m.loadFindingsCmd(activeOnlyFilters(), "All active findings"))
	case "enter":
		if in := m.selectedInsight(); in != nil {
			m.targetInsight = *in
			m.loading = true
			m.results = nil
			m.resultTable.SetRows(nil)
			m.mode = modeInsightResults
			return m, tea.Batch(m.loader.Tick(), m.loadInsightResultsCmd(*in))
		}
	}
	var cmd tea.Cmd
	m.insightTable, cmd = m.insightTable.Update(msg)
	return m, cmd
}

// --- insight results -----------------------------------------------------

func (m Model) updateResultKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeInsights
		m.results = nil
		return m, nil
	case "enter":
		if r := m.selectedResult(); r != nil {
			filters := filtersForInsightGroup(m.targetInsight, r.GroupValue)
			scope := m.targetInsight.GroupBy + " = " + r.GroupValue
			m.loading = true
			m.mode = modeFindings
			return m, tea.Batch(m.loader.Tick(), m.loadFindingsCmd(filters, scope))
		}
	}
	var cmd tea.Cmd
	m.resultTable, cmd = m.resultTable.Update(msg)
	return m, cmd
}

// --- findings ------------------------------------------------------------

func (m Model) updateFindingKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.yankPending {
		m.yankPending = false
		f := m.selectedFinding()
		if f == nil {
			m.status = "no finding selected"
			return m, nil
		}
		switch msg.String() {
		case "a":
			m.status = doYank(f.ID, "finding id")
		case "t":
			m.status = doYank(f.Title, "title")
		case "l":
			if f.Remediation.URL == "" {
				m.status = "no remediation URL"
			} else {
				m.status = doYank(f.Remediation.URL, "remediation URL")
			}
		default:
			m.status = ""
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		// Back to results if we got here via an insight, else back to
		// insights.
		if m.targetInsight.ARN != "" && len(m.results) > 0 {
			m.mode = modeInsightResults
		} else {
			m.mode = modeInsights
		}
		m.findings = nil
		m.findingsFilt = nil
		m.findingsScope = ""
		return m, nil
	case "r":
		// Re-issue the same filters as the current scope.
		m.loading = true
		filters := m.lastFilters()
		return m, tea.Batch(m.loader.Tick(), m.loadFindingsCmd(filters, m.findingsScope))
	case "1":
		m.sev.critical = !m.sev.critical
		m.applyFindingFilter()
		return m, nil
	case "2":
		m.sev.high = !m.sev.high
		m.applyFindingFilter()
		return m, nil
	case "3":
		m.sev.medium = !m.sev.medium
		m.applyFindingFilter()
		return m, nil
	case "4":
		m.sev.low = !m.sev.low
		m.applyFindingFilter()
		return m, nil
	case "5":
		m.sev.informational = !m.sev.informational
		m.applyFindingFilter()
		return m, nil
	case "s":
		m.showSuppress = !m.showSuppress
		m.applyFindingFilter()
		return m, nil
	case "y":
		m.yankPending = true
		m.status = "yank: a=arn · t=title · l=remediation URL"
		return m, nil
	case "enter":
		if f := m.selectedFinding(); f != nil {
			m.targetFinding = *f
			m.mode = modeFindingDetail
			m.status = ""
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.findingsTable, cmd = m.findingsTable.Update(msg)
	return m, cmd
}

// lastFilters rebuilds the filter set that produced the current findings
// list (so r can refresh). When we drilled from an insight result, recover
// the filters from the targetInsight + selected result. Otherwise it was an
// "all active findings" view.
func (m Model) lastFilters() shtypes.AwsSecurityFindingFilters {
	if m.targetInsight.ARN == "" {
		return activeOnlyFilters()
	}
	// Pull the group value out of the scope label; scope = "<GroupBy> = <value>".
	if i := strings.Index(m.findingsScope, " = "); i > 0 {
		return filtersForInsightGroup(m.targetInsight, m.findingsScope[i+3:])
	}
	return activeOnlyFilters()
}

// --- detail ---------------------------------------------------------------

func (m Model) updateDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.yankPending {
		m.yankPending = false
		switch msg.String() {
		case "a":
			m.status = doYank(m.targetFinding.ID, "finding id")
		case "t":
			m.status = doYank(m.targetFinding.Title, "title")
		case "l":
			if m.targetFinding.Remediation.URL == "" {
				m.status = "no remediation URL"
			} else {
				m.status = doYank(m.targetFinding.Remediation.URL, "remediation URL")
			}
		default:
			m.status = ""
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.mode = modeFindings
		m.status = ""
		return m, nil
	case "y":
		m.yankPending = true
		m.status = "yank: a=arn · t=title · l=remediation URL"
		return m, nil
	}
	return m, nil
}

// --- helpers --------------------------------------------------------------

func (m Model) CapturingInput() bool {
	return m.mode == modeInsights && m.filterMode
}

func (m Model) InSubnav() bool {
	return m.mode != modeInsights
}

func (m Model) BookmarkCurrent() (string, string, bool) {
	in := m.selectedInsight()
	if in == nil {
		return "", "", false
	}
	return in.Name, in.Name, true
}

func (m Model) SetFilterQuery(q string) tea.Model {
	m.filter.SetValue(q)
	m.applyInsightFilter()
	return m
}

func (m Model) StatusFooter() statusbar.Snapshot {
	items := len(m.insightsFilt)
	switch m.mode {
	case modeInsightResults:
		items = len(m.results)
	case modeFindings:
		items = len(m.findingsFilt)
	case modeFindingDetail:
		items = -1
	}
	return statusbar.Snapshot{
		Items:      items,
		LastLoaded: m.lastLoaded,
		Message:    m.status,
	}
}

func (m Model) HelpItems() []help.Section {
	switch m.mode {
	case modeInsightResults:
		return []help.Section{{
			Title: "SecurityHub · insight results",
			Items: []help.Item{
				{Keys: "enter", Desc: "findings for this group"},
				{Keys: "esc", Desc: "back to insights"},
			},
		}}
	case modeFindings:
		return []help.Section{{
			Title: "SecurityHub · findings",
			Items: []help.Item{
				{Keys: "enter", Desc: "finding detail"},
				{Keys: "y", Desc: "yank menu: a=arn, t=title, l=remediation URL"},
				{Keys: "1/2/3/4/5", Desc: "toggle severity (CRIT/HIGH/MED/LOW/INFO)"},
				{Keys: "s", Desc: "toggle suppressed visibility"},
				{Keys: "r", Desc: "refresh"},
				{Keys: "esc", Desc: "back"},
			},
		}}
	case modeFindingDetail:
		return []help.Section{{
			Title: "SecurityHub · finding detail",
			Items: []help.Item{
				{Keys: "y", Desc: "yank menu: a=arn, t=title, l=remediation URL"},
				{Keys: "esc", Desc: "back to findings"},
			},
		}}
	}
	if m.filterMode {
		return []help.Section{{
			Title: "SecurityHub · filter",
			Items: []help.Item{
				{Keys: "type", Desc: "match insight name"},
				{Keys: "enter", Desc: "apply and exit filter"},
				{Keys: "esc", Desc: "clear and exit filter"},
			},
		}}
	}
	return []help.Section{{
		Title: "SecurityHub insights",
		Items: []help.Item{
			{Keys: "enter", Desc: "open insight (aggregations)"},
			{Keys: "a", Desc: "skip insights, list all active findings"},
			{Keys: "/", Desc: "filter"},
			{Keys: "r", Desc: "refresh"},
		},
	}}
}

func (m *Model) applyInsightFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		m.insightsFilt = m.insights
	} else {
		out := make([]Insight, 0, len(m.insights))
		for _, in := range m.insights {
			if strings.Contains(strings.ToLower(in.Name), q) {
				out = append(out, in)
			}
		}
		m.insightsFilt = out
	}
	m.insightTable.SetRows(buildInsightRows(m.insightsFilt))
}

func (m *Model) applyFindingFilter() {
	out := make([]Finding, 0, len(m.findings))
	for _, f := range m.findings {
		if !m.sev.accepts(f.Severity) {
			continue
		}
		if f.Workflow == "SUPPRESSED" && !m.showSuppress {
			continue
		}
		out = append(out, f)
	}
	m.findingsFilt = out
	m.findingsTable.SetRows(buildFindingRows(m.findingsFilt))
}

func (m Model) selectedInsight() *Insight {
	if m.loading || len(m.insightsFilt) == 0 {
		return nil
	}
	idx := m.insightTable.Cursor()
	if idx < 0 || idx >= len(m.insightsFilt) {
		return nil
	}
	return &m.insightsFilt[idx]
}

func (m Model) selectedResult() *InsightResult {
	if len(m.results) == 0 {
		return nil
	}
	idx := m.resultTable.Cursor()
	if idx < 0 || idx >= len(m.results) {
		return nil
	}
	return &m.results[idx]
}

func (m Model) selectedFinding() *Finding {
	if len(m.findingsFilt) == 0 {
		return nil
	}
	idx := m.findingsTable.Cursor()
	if idx < 0 || idx >= len(m.findingsFilt) {
		return nil
	}
	return &m.findingsFilt[idx]
}

// --- ASFF translation -----------------------------------------------------

func toFinding(f shtypes.AwsSecurityFinding) Finding {
	out := Finding{
		ID:          awssdk.ToString(f.Id),
		Title:       awssdk.ToString(f.Title),
		Description: awssdk.ToString(f.Description),
		ProductName: awssdk.ToString(f.ProductName),
		AccountID:   awssdk.ToString(f.AwsAccountId),
		Region:      awssdk.ToString(f.Region),
		RecordState: string(f.RecordState),
	}
	if f.Severity != nil {
		out.Severity = string(f.Severity.Label)
	}
	if f.Workflow != nil {
		out.Workflow = string(f.Workflow.Status)
	}
	if t, err := time.Parse(time.RFC3339, awssdk.ToString(f.UpdatedAt)); err == nil {
		out.UpdatedAt = t
	} else if t, err := time.Parse(time.RFC3339Nano, awssdk.ToString(f.UpdatedAt)); err == nil {
		out.UpdatedAt = t
	}
	for _, r := range f.Resources {
		out.Resources = append(out.Resources, FindingResource{
			ARN:  awssdk.ToString(r.Id),
			Type: awssdk.ToString(r.Type),
		})
	}
	if f.Compliance != nil {
		out.Compliance = string(f.Compliance.Status)
		for _, sa := range f.Compliance.AssociatedStandards {
			out.Standards = append(out.Standards, awssdk.ToString(sa.StandardsId))
		}
	}
	if f.Remediation != nil && f.Remediation.Recommendation != nil {
		out.Remediation = Remediation{
			Text: awssdk.ToString(f.Remediation.Recommendation.Text),
			URL:  awssdk.ToString(f.Remediation.Recommendation.Url),
		}
	}
	return out
}

func severityRank(s string) int {
	switch s {
	case "CRITICAL":
		return 5
	case "HIGH":
		return 4
	case "MEDIUM":
		return 3
	case "LOW":
		return 2
	case "INFORMATIONAL":
		return 1
	}
	return 0
}

func humanGroupBy(g string) string {
	switch {
	case strings.HasPrefix(g, "ResourceId"):
		return "Resource"
	case strings.HasPrefix(g, "Severity"):
		return "Severity"
	case strings.HasPrefix(g, "AwsAccountId"):
		return "Account"
	case strings.HasPrefix(g, "ProductName"):
		return "Product"
	case strings.HasPrefix(g, "UserName") || strings.Contains(g, "Principal") || strings.Contains(g, "IamUser"):
		return "Principal"
	case strings.HasPrefix(g, "Type"):
		return "Type"
	}
	return g
}

// insightOwner returns "AWS" for managed insight ARNs (empty account
// segment) and "You" for account-owned insights.
func insightOwner(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) >= 5 && parts[4] == "" {
		return "AWS"
	}
	return "You"
}

func activeOnlyFilters() shtypes.AwsSecurityFindingFilters {
	return shtypes.AwsSecurityFindingFilters{
		RecordState: []shtypes.StringFilter{{
			Value:      awssdk.String("ACTIVE"),
			Comparison: shtypes.StringFilterComparisonEquals,
		}},
	}
}

func filtersForInsightGroup(insight Insight, groupValue string) shtypes.AwsSecurityFindingFilters {
	eq := []shtypes.StringFilter{{
		Value:      awssdk.String(groupValue),
		Comparison: shtypes.StringFilterComparisonEquals,
	}}
	f := activeOnlyFilters()
	switch insight.GroupBy {
	case "Resource":
		f.ResourceId = eq
	case "Account":
		f.AwsAccountId = eq
	case "Product":
		f.ProductName = eq
	case "Severity":
		f.SeverityLabel = eq
	case "Principal":
		f.ResourceAwsIamUserUserName = eq
	case "Type":
		f.Type = eq
	}
	return f
}

// wrapAccessErr produces a helpful message when SecurityHub is not enabled
// in the region. The SDK returns *shtypes.InvalidAccessException for both
// "not subscribed" and genuine permission denials; the message body usually
// disambiguates.
func wrapAccessErr(err error) error {
	var inv *shtypes.InvalidAccessException
	if errors.As(err, &inv) {
		return fmt.Errorf("SecurityHub may not be enabled in this region (or your principal lacks permission). Enable via the console or run: aws securityhub enable-security-hub\nOriginal: %s", inv.Error())
	}
	return err
}

func doYank(value, label string) string {
	if err := clipboard.WriteAll(value); err != nil {
		return "clipboard error: " + err.Error()
	}
	return "copied " + label
}

// --- row builders ---------------------------------------------------------

func buildInsightRows(items []Insight) []datatable.Row {
	rows := make([]datatable.Row, len(items))
	for i, in := range items {
		rows[i] = datatable.Row{in.Name, in.GroupBy, in.Owner}
	}
	return rows
}

func buildResultRows(items []InsightResult) []datatable.Row {
	rows := make([]datatable.Row, len(items))
	for i, r := range items {
		rows[i] = datatable.Row{r.GroupValue, strconv.FormatInt(r.Count, 10)}
	}
	return rows
}

func buildFindingRows(items []Finding) []datatable.Row {
	rows := make([]datatable.Row, len(items))
	for i, f := range items {
		rows[i] = datatable.Row{
			renderSeverity(f.Severity),
			f.Title,
			f.ProductName,
			ageLabel(f.UpdatedAt),
		}
	}
	return rows
}

func renderSeverity(s string) string {
	var style lipgloss.Style
	switch s {
	case "CRITICAL":
		style = lipgloss.NewStyle().Background(lipgloss.Color("160")).Foreground(lipgloss.Color("231")).Bold(true)
		return style.Render(" CRIT ")
	case "HIGH":
		style = lipgloss.NewStyle().Background(lipgloss.Color("208")).Foreground(lipgloss.Color("232")).Bold(true)
		return style.Render(" HIGH ")
	case "MEDIUM":
		style = lipgloss.NewStyle().Background(lipgloss.Color("214")).Foreground(lipgloss.Color("232"))
		return style.Render(" MED  ")
	case "LOW":
		style = lipgloss.NewStyle().Background(lipgloss.Color("33")).Foreground(lipgloss.Color("231"))
		return style.Render(" LOW  ")
	case "INFORMATIONAL":
		return mutedStyle.Render(" INFO ")
	}
	return mutedStyle.Render("  -   ")
}

func ageLabel(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < 0 {
		return "0s"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// --- view -----------------------------------------------------------------

func (m Model) View() string {
	if m.err != nil {
		hint := "Press r to retry · esc to back."
		if _, ok := m.err.(*awspkg.SSOExpiredError); ok {
			hint = "Run `aws sso login --profile " + m.ctx.Profile + "` then press r."
		}
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\n%s", m.err, hint))
	}
	switch m.mode {
	case modeInsightResults:
		return m.viewResults()
	case modeFindings:
		return m.viewFindings()
	case modeFindingDetail:
		return m.viewDetail()
	}
	return m.viewInsights()
}

func (m Model) viewInsights() string {
	if m.loading {
		return m.loader.Render("loading insights...")
	}
	if len(m.insights) == 0 {
		title := headerStyle.Render("SecurityHub: no insights returned")
		body := mutedStyle.Render(strings.Join([]string{
			"GetInsights succeeded but returned 0 records. Likely causes:",
			"",
			"  · SecurityHub is not enabled in " + m.ctx.Region + ".",
			"    Enable with: aws securityhub enable-security-hub --region " + m.ctx.Region + " --profile " + m.ctx.Profile,
			"",
			"  · Your principal can call DescribeHub but not GetInsights",
			"    (rare — usually a custom IAM policy that omits securityhub:GetInsights).",
			"",
			"  · All managed insights have been disabled in the console.",
			"",
			"Press 'a' to skip insights and call GetFindings directly. If that",
			"also returns 0 (or errors with InvalidAccessException) you'll get",
			"a clearer signal about which of the above applies.",
		}, "\n"))
		help := mutedStyle.Render("a: try all findings · r: retry · ←/→ or tab: other tabs")
		return lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", help)
	}
	header := headerStyle.Render(fmt.Sprintf("SecurityHub Insights (%d)", len(m.insights)))
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}
	body := m.insightTable.View()
	if len(m.insightsFilt) == 0 && m.filter.Value() != "" {
		body = mutedStyle.Render("No insights match filter.")
	}
	help := mutedStyle.Render("enter: open · a: all findings · /: filter · r: refresh")
	parts := []string{header, filterLine, "", body, "", help}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) viewResults() string {
	if m.loading {
		return m.loader.Render("loading insight results...")
	}
	title := headerStyle.Render(m.targetInsight.Name)
	totalMatched := int64(0)
	for _, r := range m.results {
		totalMatched += r.Count
	}
	meta := mutedStyle.Render(fmt.Sprintf("Grouped by: %s    Total findings matched: %d", m.targetInsight.GroupBy, totalMatched))
	body := m.resultTable.View()
	if len(m.results) == 0 {
		body = mutedStyle.Render("(no results)")
	}
	help := mutedStyle.Render("enter: see findings for this group · esc: back")
	return lipgloss.JoinVertical(lipgloss.Left, title, meta, body, help)
}

func (m Model) viewFindings() string {
	if m.loading {
		return m.loader.Render("loading findings...")
	}
	title := headerStyle.Render(fmt.Sprintf("Findings: %s (%d)", m.findingsScope, len(m.findingsFilt)))
	toggles := renderToggles(m.sev, m.showSuppress)
	body := m.findingsTable.View()
	if len(m.findingsFilt) == 0 {
		body = mutedStyle.Render("No findings match the current filters.")
	}
	help := mutedStyle.Render("enter: details · y a/t/l: yank · 1-5 sev toggle · s suppressed · esc back")
	parts := []string{title, toggles}
	if m.findingsCapped {
		parts = append(parts, mutedStyle.Render(fmt.Sprintf("showing first %d - refine via the insight", maxFindingsFetch)))
	}
	parts = append(parts, body, help)
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func renderToggles(s sevFilter, suppress bool) string {
	cells := []struct {
		key   string
		label string
		on    bool
	}{
		{"1", "Crit", s.critical},
		{"2", "High", s.high},
		{"3", "Med", s.medium},
		{"4", "Low", s.low},
		{"5", "Info", s.informational},
	}
	var parts []string
	for _, c := range cells {
		label := fmt.Sprintf("[%s %s]", c.key, c.label)
		style := mutedStyle
		if c.on {
			style = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
		}
		parts = append(parts, style.Render(label))
	}
	sLabel := "[s Suppressed]"
	sStyle := mutedStyle
	if suppress {
		sStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	}
	parts = append(parts, sStyle.Render(sLabel))
	return strings.Join(parts, "  ")
}

func (m Model) viewDetail() string {
	f := m.targetFinding
	title := headerStyle.Render(f.Title)
	meta := strings.Join([]string{
		field("Severity", renderSeverity(f.Severity)),
		field("Workflow", emptyDash(f.Workflow)),
		field("State", emptyDash(f.RecordState)),
		field("Updated", formatTimeT(f.UpdatedAt)),
		field("Product", emptyDash(f.ProductName)),
		field("Region", emptyDash(f.Region)),
		field("Account", emptyDash(f.AccountID)),
	}, "\n")

	var resBuilder strings.Builder
	for _, r := range f.Resources {
		resBuilder.WriteString("  ")
		resBuilder.WriteString(r.ARN)
		if r.Type != "" {
			resBuilder.WriteString("  (")
			resBuilder.WriteString(r.Type)
			resBuilder.WriteString(")")
		}
		resBuilder.WriteString("\n")
	}
	resources := strings.TrimRight(resBuilder.String(), "\n")
	if resources == "" {
		resources = mutedStyle.Render("  (none)")
	}

	compliance := emptyDash(f.Compliance)
	standards := "-"
	if len(f.Standards) > 0 {
		standards = strings.Join(f.Standards, ", ")
	}

	descBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(boxWidth(m.width)).
		Render(emptyDash(f.Description))

	remText := emptyDash(f.Remediation.Text)
	if f.Remediation.URL != "" {
		remText += "\n" + mutedStyle.Render("URL: ") + f.Remediation.URL
	}
	remBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(boxWidth(m.width)).
		Render(remText)

	help := mutedStyle.Render("y a: yank id · y t: yank title · y l: yank remediation URL · esc back")

	parts := []string{
		title, "",
		meta, "",
		mutedStyle.Render("Resources:"),
		resources, "",
		field("Compliance", compliance),
		field("Standards", standards), "",
		mutedStyle.Render("Description:"),
		descBox, "",
		mutedStyle.Render("Remediation:"),
		remBox, "",
		help,
	}
	if m.status != "" {
		parts = append(parts, mutedStyle.Render(m.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func field(label, value string) string {
	return fmt.Sprintf("  %-12s %s", label+":", value)
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func formatTimeT(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func boxWidth(viewWidth int) int {
	if viewWidth <= 4 {
		return 40
	}
	w := viewWidth - 2
	if w < 20 {
		return 20
	}
	return w
}

var (
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle = lipgloss.NewStyle().Bold(true)
)
