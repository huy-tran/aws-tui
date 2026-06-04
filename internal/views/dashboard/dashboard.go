package dashboard

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awspkg "github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/state"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/statusbar"
	"github.com/huy-tran/aws-tui/internal/views/beanstalk"
	"github.com/huy-tran/aws-tui/internal/views/bookmarks"
	"github.com/huy-tran/aws-tui/internal/views/cloudfront"
	"github.com/huy-tran/aws-tui/internal/views/cloudwatch"
	"github.com/huy-tran/aws-tui/internal/views/codedeploy"
	"github.com/huy-tran/aws-tui/internal/views/ec2"
	"github.com/huy-tran/aws-tui/internal/views/paramstore"
	"github.com/huy-tran/aws-tui/internal/views/rds"
	"github.com/huy-tran/aws-tui/internal/views/s3"
	"github.com/huy-tran/aws-tui/internal/views/securityhub"
)

// inputCaptor is implemented by tab views that have modes where keystrokes
// must reach a text input rather than the dashboard's tab-switch hotkeys.
type inputCaptor interface {
	CapturingInput() bool
}

// subnavCaptor is implemented by tab views that have sub-screens (e.g.
// Beanstalk details, CloudFront invalidations) where left/right arrows mean
// something local and should not switch dashboard tabs. tab/shift+tab remain
// the universal escape hatch.
type subnavCaptor interface {
	InSubnav() bool
}

type Tab int

const (
	TabBeanstalk Tab = iota
	TabEC2
	TabRDS
	TabLogs
	TabCloudFront
	TabS3
	TabParamStore
	TabSecurityHub
	TabCodeDeploy
)

var tabNames = []string{"Beanstalk", "EC2", "RDS", "Logs", "CloudFront", "S3", "Parameter Store", "SecurityHub", "CodeDeploy"}

type Model struct {
	ctx    *awspkg.Context
	store  *state.Store
	active Tab
	tabs   [9]tea.Model
	width  int
	height int
	status string
}

// Bookmarkable is implemented by tabs that have a currently-selected
// resource we can bookmark.
type Bookmarkable interface {
	BookmarkCurrent() (label, id string, ok bool)
}

// FilterTarget is implemented by tabs that have a filter input; the
// dashboard uses it to pre-fill the filter after jumping to a bookmark.
// Returns the updated tea.Model because tabs are stored by value in the
// interface and a pointer-receiver mutation would be lost.
type FilterTarget interface {
	SetFilterQuery(q string) tea.Model
}

func New(ctx *awspkg.Context, store *state.Store) Model {
	m := Model{ctx: ctx, store: store, active: TabBeanstalk}
	m.tabs[TabBeanstalk] = beanstalk.New(ctx)
	m.tabs[TabEC2] = ec2.New(ctx)
	m.tabs[TabRDS] = rds.New(ctx)
	m.tabs[TabLogs] = cloudwatch.New(ctx)
	m.tabs[TabCloudFront] = cloudfront.New(ctx)
	m.tabs[TabS3] = s3.New(ctx, store)
	m.tabs[TabParamStore] = paramstore.New(ctx)
	m.tabs[TabSecurityHub] = securityhub.New(ctx)
	m.tabs[TabCodeDeploy] = codedeploy.New(ctx)
	return m
}

func (m Model) Init() tea.Cmd {
	// Only initialise the active tab; others run Init on first activation.
	return m.tabs[m.active].Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ch, ok := msg.(bookmarks.ChosenMsg); ok {
		// User picked a bookmark in the pushed list view. Switch to that
		// tab and pre-fill its filter so the bookmarked row lands in
		// view. Tab name lookup tolerates a missing tab gracefully
		// (legacy bookmarks from a removed tab simply do nothing).
		for i, name := range tabNames {
			if name == ch.Tab {
				prev := m.active
				m.active = Tab(i)
				if ft, ok := m.tabs[m.active].(FilterTarget); ok {
					m.tabs[m.active] = ft.SetFilterQuery(ch.ID)
				}
				if prev != m.active {
					return m, m.tabs[m.active].Init()
				}
				return m, nil
			}
		}
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		inner := tea.WindowSizeMsg{
			Width:  msg.Width,
			Height: msg.Height - 7, // header + tabs + footer + breathing room + three spacer rows
		}
		for i := range m.tabs {
			updated, _ := m.tabs[i].Update(inner)
			m.tabs[i] = updated
		}
		return m, nil

	case tea.KeyMsg:
		capInput := false
		if c, ok := m.tabs[m.active].(inputCaptor); ok {
			capInput = c.CapturingInput()
		}
		inSubnav := false
		if s, ok := m.tabs[m.active].(subnavCaptor); ok {
			inSubnav = s.InSubnav()
		}
		// tab/shift+tab cycle dashboard tabs, but only when the active tab
		// is not capturing text input. Inside a form (paramstore edit,
		// cloudwatch search, rds port-forward, etc.) tab must reach the
		// view so it can cycle form fields. To switch dashboard tabs from
		// inside a form, esc out first.
		if !capInput {
			switch msg.String() {
			case "tab":
				return m.switchTab((m.active + 1) % Tab(len(m.tabs)))
			case "shift+tab":
				return m.switchTab((m.active - 1 + Tab(len(m.tabs))) % Tab(len(m.tabs)))
			}
		}
		// Left/right arrows also cycle tabs, but only when the active tab is
		// on its root list and not capturing text input. In a sub-screen or
		// with an input focused, arrows belong to the view (cursor movement,
		// horizontal scroll, etc.).
		if msg.String() == "left" || msg.String() == "right" {
			if !capInput && !inSubnav {
				if msg.String() == "right" {
					return m.switchTab((m.active + 1) % Tab(len(m.tabs)))
				}
				return m.switchTab((m.active - 1 + Tab(len(m.tabs))) % Tab(len(m.tabs)))
			}
		}
		// Bookmarks: 'b' toggles a bookmark for the current row, 'B'
		// opens the bookmarks list. Both gated by !capturing && !subnav
		// so they don't collide with filter input or sub-mode bindings
		// (S3's 'y b' yank chord, etc).
		if !capInput && !inSubnav {
			switch msg.String() {
			case "b":
				if bm, ok := m.tabs[m.active].(Bookmarkable); ok {
					if label, id, ok := bm.BookmarkCurrent(); ok {
						profile := ""
						region := ""
						if m.ctx != nil {
							profile = m.ctx.Profile
							region = m.ctx.Region
						}
						tabName := tabNames[m.active]
						if m.store.IsBookmarked(profile, tabName, id) {
							m.store.RemoveBookmark(profile, tabName, id)
							_ = m.store.Save()
							m.status = "removed bookmark " + label
						} else {
							m.store.AddBookmark(profile, state.Bookmark{
								Tab:    tabName,
								Region: region,
								ID:     id,
								Label:  label,
								AddedAt: time.Now().UTC().Format(time.RFC3339),
							})
							_ = m.store.Save()
							m.status = "bookmarked " + label
						}
					}
				}
				return m, nil
			case "B":
				profile := ""
				region := ""
				if m.ctx != nil {
					profile = m.ctx.Profile
					region = m.ctx.Region
				}
				return m, nav.PushView(bookmarks.New(m.store, profile, region))
			}
		}
	}

	updated, cmd := m.tabs[m.active].Update(msg)
	m.tabs[m.active] = updated
	return m, cmd
}

// CapturingInput delegates to the active tab. Lets the app know whether
// a global key like '?' should be intercepted or passed through to an input.
func (m Model) CapturingInput() bool {
	if c, ok := m.tabs[m.active].(inputCaptor); ok {
		return c.CapturingInput()
	}
	return false
}

// StatusFooter reports the active tab's name plus whatever item count /
// freshness / message the tab itself surfaces. Dashboard-level status
// (bookmark add / remove) wins over the tab's status when both are set.
func (m Model) StatusFooter() statusbar.Snapshot {
	s := statusbar.Snapshot{Items: -1, View: tabNames[m.active]}
	if r, ok := m.tabs[m.active].(statusbar.Reporter); ok {
		sub := r.StatusFooter()
		s.Items = sub.Items
		s.LastLoaded = sub.LastLoaded
		s.Message = sub.Message
	}
	if m.status != "" {
		s.Message = m.status
	}
	return s
}

// HelpItems composes the help sections for the current active tab.
func (m Model) HelpItems() []help.Section {
	var sections []help.Section
	if p, ok := m.tabs[m.active].(help.Provider); ok {
		sections = append(sections, p.HelpItems()...)
	}
	return sections
}

func (m Model) switchTab(t Tab) (tea.Model, tea.Cmd) {
	if t == m.active {
		return m, nil
	}
	m.active = t
	return m, m.tabs[t].Init()
}

func (m Model) View() string {
	header := m.renderHeader()
	tabs := m.renderTabs()
	footer := m.renderFooter()

	// Three blank spacer rows: between the header and the tab bar, between
	// the tab bar and the table, and between the table and the footer.
	// Subtract them so the content still fits the available height.
	contentHeight := m.height - lipgloss.Height(header) - lipgloss.Height(tabs) - lipgloss.Height(footer) - 3
	content := lipgloss.NewStyle().Height(contentHeight).Render(m.tabs[m.active].View())

	return lipgloss.JoinVertical(lipgloss.Left, header, "", tabs, "", content, "", footer)
}

func (m Model) renderHeader() string {
	profileStyle := profileColour(m.ctx.Profile).
		Padding(0, 1).
		Bold(true)
	regionStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("237")).
		Foreground(lipgloss.Color("252")).
		Padding(0, 1)
	serviceStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("240")).
		Foreground(lipgloss.Color("252")).
		Padding(0, 1)

	return lipgloss.JoinHorizontal(lipgloss.Top,
		profileStyle.Render(m.ctx.Profile),
		regionStyle.Render(m.ctx.Region),
		serviceStyle.Render(tabNames[m.active]),
	)
}

func (m Model) renderTabs() string {
	var parts []string
	for i, name := range tabNames {
		style := lipgloss.NewStyle().Padding(0, 2)
		if Tab(i) == m.active {
			style = style.Bold(true).Underline(true).Foreground(lipgloss.Color("213"))
		} else {
			style = style.Foreground(lipgloss.Color("245"))
		}
		parts = append(parts, style.Render(name))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func (m Model) renderFooter() string {
	help := "↑/↓ nav · ←/→ or tab tabs · enter open · r refresh · ctrl+p profile · ctrl+r region · ? help · ctrl+c quit"
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(0, 1).
		Render(help)
}

// profileColour returns a background style flagging the environment risk
// level. This is the single most important safety affordance: a user looking
// at red knows they are in prod and will think twice before destructive actions.
func profileColour(profile string) lipgloss.Style {
	p := strings.ToLower(profile)
	switch {
	case strings.HasPrefix(p, "prod"):
		return lipgloss.NewStyle().
			Background(lipgloss.Color("160")). // red
			Foreground(lipgloss.Color("231"))
	case strings.HasPrefix(p, "staging"):
		return lipgloss.NewStyle().
			Background(lipgloss.Color("214")). // orange/yellow
			Foreground(lipgloss.Color("232"))
	case strings.HasPrefix(p, "dev"), strings.HasPrefix(p, "test"):
		return lipgloss.NewStyle().
			Background(lipgloss.Color("34")). // green
			Foreground(lipgloss.Color("231"))
	default:
		return lipgloss.NewStyle().
			Background(lipgloss.Color("33")). // blue
			Foreground(lipgloss.Color("231"))
	}
}
