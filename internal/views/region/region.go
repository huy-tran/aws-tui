package region

import (
	"context"
	"encoding/gob"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/state"
)

const optInCacheTTL = 30 * time.Minute

func init() { gob.Register(map[string]string{}) }

type regionItem struct {
	code  string
	name  string
	badge string // styled badge string; empty until probe completes
}

func (r regionItem) Title() string { return r.code }
func (r regionItem) Description() string {
	if r.badge == "" {
		return r.name
	}
	return r.name + "  " + r.badge
}
func (r regionItem) FilterValue() string { return r.code + " " + r.name }

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
	list       list.Model
	customMode bool
	custom     textinput.Model
	badges     map[string]string
}

type (
	probeResultMsg struct{ badges map[string]string }
	probeErrMsg    struct{ err error }
)

func New(ctx *awspkg.Context, store *state.Store) Model {
	items := make([]list.Item, 0, len(awspkg.CommonRegions))
	for _, code := range awspkg.CommonRegions {
		items = append(items, regionItem{code: code, name: regionNames[code]})
	}

	delegate := list.NewDefaultDelegate()
	l := list.New(items, delegate, 0, 0)
	l.Title = "Select Region (profile: " + ctx.Profile + ")"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)

	if lastRegion := store.LastRegionFor(ctx.Profile); lastRegion != "" {
		for i, item := range items {
			if ri, ok := item.(regionItem); ok && ri.code == lastRegion {
				l.Select(i)
				break
			}
		}
	}

	ti := textinput.New()
	ti.Placeholder = "e.g. me-south-1"
	ti.CharLimit = 20

	return Model{ctx: ctx, state: store, list: l, custom: ti}
}

func (m Model) Init() tea.Cmd {
	return m.probeRegionsCmd()
}

// probeRegionsCmd calls ec2.DescribeRegions once at picker entry so the
// list can show which regions the active profile can actually hit.
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
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width-4, msg.Height-4)
		return m, nil

	case probeResultMsg:
		m.badges = msg.badges
		items := make([]list.Item, 0, len(awspkg.CommonRegions))
		for _, code := range awspkg.CommonRegions {
			items = append(items, regionItem{
				code:  code,
				name:  regionNames[code],
				badge: m.badges[code],
			})
		}
		_ = m.list.SetItems(items)
		return m, nil

	case probeErrMsg:
		// Best-effort: degrade silently to no-badge listing.
		return m, nil

	case tea.KeyMsg:
		if m.customMode {
			switch msg.String() {
			case "enter":
				region := m.custom.Value()
				if region != "" {
					return m, m.selectRegion(region)
				}
			case "esc":
				m.customMode = false
				return m, nil
			}
			var cmd tea.Cmd
			m.custom, cmd = m.custom.Update(msg)
			return m, cmd
		}

		if m.list.FilterState() == list.Filtering {
			break
		}
		switch msg.String() {
		case "enter":
			if it, ok := m.list.SelectedItem().(regionItem); ok {
				return m, m.selectRegion(it.code)
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
	m.list, cmd = m.list.Update(msg)
	return m, cmd
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
		return lipgloss.NewStyle().Padding(1, 2).Render(
			"Enter region code:\n\n" + m.custom.View() + "\n\nenter to confirm · esc to cancel",
		)
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(m.list.View())
}
