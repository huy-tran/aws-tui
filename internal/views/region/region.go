package region

import (
	"context"
	"encoding/gob"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/state"
	"github.com/huy-tran/aws-tui/internal/ui/datatable"
)

const optInCacheTTL = 30 * time.Minute

func init() { gob.Register(map[string]string{}) }

var regionNames = map[string]string{
	"ap-southeast-2": "Sydney",
	"ap-southeast-1": "Singapore",
	"ap-southeast-4": "Melbourne",
	"ap-northeast-1": "Tokyo",
	"us-east-1":      "N. Virginia",
	"us-east-2":      "Ohio",
	"us-west-1":      "N. California",
	"us-west-2":      "Oregon",
	"eu-west-1":      "Ireland",
	"eu-west-2":      "London",
	"eu-central-1":   "Frankfurt",
}

type Model struct {
	ctx        *awspkg.Context
	state      *state.Store
	table      datatable.Model
	filter     textinput.Model
	filterMode bool
	customMode bool
	custom     textinput.Model
	badges     map[string]string // region code -> styled opt-in badge
	displayed  []string          // visible region codes after filter
	width      int
	height     int
}

type (
	probeResultMsg struct{ badges map[string]string }
	probeErrMsg    struct{ err error }
)

func New(ctx *awspkg.Context, store *state.Store) Model {
	cols := []datatable.Column{
		{Title: "Region"},
		{Title: "Name", Flex: true},
		{Title: "Status"},
	}
	t := datatable.New(cols)
	t.SetHeight(20)

	flt := textinput.New()
	flt.Placeholder = "filter by code / name"
	flt.CharLimit = 32
	flt.Prompt = "/ "

	ti := textinput.New()
	ti.Placeholder = "e.g. me-south-1"
	ti.CharLimit = 20

	m := Model{ctx: ctx, state: store, table: t, filter: flt, custom: ti}
	m.applyFilter()
	if last := store.LastRegionFor(ctx.Profile); last != "" {
		for i, code := range m.displayed {
			if code == last {
				m.table.SetCursor(i)
				break
			}
		}
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return m.probeRegionsCmd()
}

// probeRegionsCmd calls ec2.DescribeRegions once at picker entry so the
// table can show which regions the active profile can actually hit.
// Cached at the aws.Cache layer; degrades to "no badges" on any error.
func (m Model) probeRegionsCmd() tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		key := "ec2:opt-in-regions"
		if ctx.Cache != nil {
			if cached, ok := ctx.Cache.Get(key); ok {
				if b, ok := cached.(map[string]string); ok {
					return probeResultMsg{badges: b}
				}
			}
		}
		if err := ctx.Load(context.Background()); err != nil {
			return probeErrMsg{err: err}
		}
		client := ctx.EC2()
		out, err := client.DescribeRegions(context.Background(), &ec2sdk.DescribeRegionsInput{
			AllRegions: awssdk.Bool(true),
		})
		if err != nil {
			return probeErrMsg{err: err}
		}
		badges := make(map[string]string, len(out.Regions))
		for _, r := range out.Regions {
			name := awssdk.ToString(r.RegionName)
			switch awssdk.ToString(r.OptInStatus) {
			case "opt-in-not-required", "opted-in":
				badges[name] = okBadge
			case "not-opted-in":
				badges[name] = optInBadge
			}
		}
		if ctx.Cache != nil {
			ctx.Cache.Set(key, badges, optInCacheTTL)
		}
		return probeResultMsg{badges: badges}
	}
}

var (
	okBadge    = lipgloss.NewStyle().Foreground(lipgloss.Color("34")).Bold(true).Render("✓")
	optInBadge = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true).Render("⚠ opt-in needed")

	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle = lipgloss.NewStyle().Bold(true)
)

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
		return m, nil

	case probeResultMsg:
		m.badges = msg.badges
		m.applyFilter()
		return m, nil

	case probeErrMsg:
		// Best-effort: degrade silently to a no-badge listing.
		return m, nil

	case tea.KeyMsg:
		if m.customMode {
			switch msg.String() {
			case "enter":
				region := strings.TrimSpace(m.custom.Value())
				if region != "" {
					return m, m.selectRegion(region)
				}
			case "esc":
				m.customMode = false
				m.custom.Blur()
				return m, nil
			}
			var cmd tea.Cmd
			m.custom, cmd = m.custom.Update(msg)
			return m, cmd
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
		case "/":
			m.filterMode = true
			m.filter.Focus()
			return m, textinput.Blink
		case "enter":
			if code := m.selected(); code != "" {
				return m, m.selectRegion(code)
			}
		case "+":
			m.customMode = true
			m.custom.Focus()
			return m, textinput.Blink
		case "esc":
			return m, nav.PopView()
		}
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// applyFilter rebuilds m.displayed and the table rows from the current filter
// text and the latest opt-in badges.
func (m *Model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	out := make([]string, 0, len(awspkg.CommonRegions))
	rows := make([]datatable.Row, 0, len(awspkg.CommonRegions))
	for _, code := range awspkg.CommonRegions {
		name := regionNames[code]
		if q != "" && !strings.Contains(strings.ToLower(code), q) && !strings.Contains(strings.ToLower(name), q) {
			continue
		}
		out = append(out, code)
		rows = append(rows, datatable.Row{code, name, m.badges[code]})
	}
	m.displayed = out
	m.table.SetRows(rows)
}

func (m Model) selected() string {
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.displayed) {
		return ""
	}
	return m.displayed[idx]
}

// CapturingInput tells the app whether a focused input is consuming text so
// global keys like '?' aren't swallowed.
func (m Model) CapturingInput() bool {
	return m.filterMode || m.customMode || m.table.CapturingInput()
}

func (m Model) selectRegion(region string) tea.Cmd {
	m.state.SetLastRegionFor(m.ctx.Profile, region)
	_ = m.state.Save()
	return func() tea.Msg {
		return nav.RegionSelectedMsg{Region: region}
	}
}

func (m Model) View() string {
	if m.customMode {
		return lipgloss.JoinVertical(lipgloss.Left,
			headerStyle.Render("Enter region code"),
			"",
			m.custom.View(),
			"",
			mutedStyle.Render("enter: confirm · esc: cancel"),
		)
	}
	header := headerStyle.Render("Select Region (profile: " + m.ctx.Profile + ")")
	filterLine := m.filter.View()
	if !m.filterMode && m.filter.Value() == "" {
		filterLine = mutedStyle.Render("press / to filter")
	}
	body := m.table.View()
	if len(m.displayed) == 0 {
		body = mutedStyle.Render("No regions match filter.")
	}
	help := mutedStyle.Render("enter: select · +: custom region · /: filter · esc: back")
	return lipgloss.JoinVertical(lipgloss.Left, header, filterLine, "", body, "", help)
}
