package app

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/huy-tran/aws-tui/internal/audit"
	"github.com/huy-tran/aws-tui/internal/auth"
	"github.com/huy-tran/aws-tui/internal/aws"
	"github.com/huy-tran/aws-tui/internal/nav"
	"github.com/huy-tran/aws-tui/internal/state"
	"github.com/huy-tran/aws-tui/internal/ui/help"
	"github.com/huy-tran/aws-tui/internal/ui/statusbar"
	"github.com/huy-tran/aws-tui/internal/views/dashboard"
	"github.com/huy-tran/aws-tui/internal/views/profile"
	"github.com/huy-tran/aws-tui/internal/views/region"
)

// Version is populated by main from ldflags at build time. The empty
// default keeps the title compact for `go run` and unit tests.
var Version string

// appTitle composes the persistent header text shown at the very top of
// every screen. "AWS TUI - vX.Y.Z" when a tagged version is baked in,
// otherwise just "AWS TUI" so dev builds aren't labelled "AWS TUI - vdev".
func appTitle() string {
	if Version != "" && Version != "dev" {
		return "AWS TUI - " + Version
	}
	return "AWS TUI"
}

// inputCaptor matches the interface dashboard / pushed views implement to
// signal that the active surface is consuming text input. Mirrored here so
// the app can decide whether to swallow '?' as a help toggle.
type inputCaptor interface {
	CapturingInput() bool
}

// Model is the root Bubble Tea model. It owns a view stack; the top of the
// stack is the active view. Navigation is via nav.PushViewMsg / nav.PopViewMsg.
//
// awsCtx is set when the user selects a profile and reused across views in
// that session.
type Model struct {
	stack    []tea.Model
	state    *state.Store
	awsCtx   *aws.Context
	width    int
	height   int
	helpOpen bool
}

func New() (Model, error) {
	store, err := state.Load()
	if err != nil {
		return Model{}, err
	}
	picker := profile.New(store)
	return Model{
		stack: []tea.Model{picker},
		state: store,
	}, nil
}

func (m Model) Init() tea.Cmd {
	if len(m.stack) == 0 {
		return nil
	}
	return m.stack[len(m.stack)-1].Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Reserve one row at the top for the app title bar and one at the
		// bottom for the status bar.
		inner := tea.WindowSizeMsg{Width: msg.Width, Height: msg.Height - 2}
		// Lower views need dimensions for when they re-render after a pop.
		for i, v := range m.stack {
			updated, _ := v.Update(inner)
			m.stack[i] = updated
		}
		return m, nil

	case tea.KeyMsg:
		// '?' toggles the help overlay - but only when no focused input
		// is consuming text. If a filter / textarea is open, the literal
		// '?' should reach the field.
		if msg.String() == keyToggleHelp && !m.topCapturingInput() {
			m.helpOpen = !m.helpOpen
			return m, nil
		}
		if m.helpOpen {
			// While the overlay is up, every other key is a no-op except
			// esc / '?' (handled above) so the underlying view does not
			// advance state behind the modal.
			if msg.String() == "esc" {
				m.helpOpen = false
			}
			return m, nil
		}
		switch msg.String() {
		case keyQuit:
			return m, tea.Quit
		case keyLock:
			// Wipe the unlock marker and quit in one stroke. Next launch
			// re-prompts for TOTP regardless of how long ago the user
			// authed. Errors are swallowed - the worst case is "lock
			// didn't take" which the user notices on next launch.
			_ = auth.Lock()
			return m, tea.Quit
		case keyProfilePicker:
			return m.resetToProfilePicker()
		case keyRegionPicker:
			if m.awsCtx != nil {
				return m.resetToRegionPicker()
			}
			// esc is intentionally NOT handled here. Each view owns its own
			// esc semantics (clear filter, back out of a sub-mode, etc.) and
			// emits nav.PopView() when it actually wants to leave the stack.
		}

	case nav.PushViewMsg:
		sized := m.sizeView(msg.View)
		m.stack = append(m.stack, sized)
		return m, sized.Init()

	case nav.PopViewMsg:
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
		return m, nil

	case nav.ProfileSelectedMsg:
		m.awsCtx = aws.NewContext(msg.Profile)
		m.state.LastProfile = msg.Profile
		_ = m.state.Save()
		return m, nav.PushView(region.New(m.awsCtx, m.state))

	case nav.RegionSelectedMsg:
		if m.awsCtx == nil {
			return m, nil
		}
		m.awsCtx.SetRegion(msg.Region)
		m.state.SetLastRegionFor(m.awsCtx.Profile, msg.Region)
		_ = m.state.Save()
		return m, nav.PushView(dashboard.New(m.awsCtx, m.state))
	}

	// Delegate everything else to the active view.
	if len(m.stack) > 0 {
		top := len(m.stack) - 1
		updated, cmd := m.stack[top].Update(msg)
		m.stack[top] = updated
		return m, cmd
	}

	return m, nil
}

func (m Model) View() string {
	title := m.renderTitle()
	if len(m.stack) == 0 {
		footer := statusbar.Render(m.statusSnapshot(), m.width)
		return lipgloss.JoinVertical(lipgloss.Left, title, footer)
	}
	body := m.stack[len(m.stack)-1].View()
	footer := statusbar.Render(m.statusSnapshot(), m.width)
	base := lipgloss.JoinVertical(lipgloss.Left, title, body, footer)
	if !m.helpOpen {
		return base
	}
	overlay := help.Render(m.helpSections())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, overlay)
}

// statusSnapshot pulls the view's reported Snapshot (if any) and merges in
// the profile / region / view-name the app itself knows.
func (m Model) statusSnapshot() statusbar.Snapshot {
	s := statusbar.Snapshot{Items: -1}
	if len(m.stack) > 0 {
		if r, ok := m.stack[len(m.stack)-1].(statusbar.Reporter); ok {
			s = r.StatusFooter()
		}
	}
	if m.awsCtx != nil {
		if s.Profile == "" {
			s.Profile = m.awsCtx.Profile
		}
		if s.Region == "" {
			s.Region = m.awsCtx.Region
		}
	}
	return s
}

func (m Model) helpSections() []help.Section {
	sections := []help.Section{help.GlobalSection()}
	if len(m.stack) > 0 {
		if p, ok := m.stack[len(m.stack)-1].(help.Provider); ok {
			sections = append(sections, p.HelpItems()...)
		}
	}
	return sections
}

func (m Model) topCapturingInput() bool {
	if len(m.stack) == 0 {
		return false
	}
	if c, ok := m.stack[len(m.stack)-1].(inputCaptor); ok {
		return c.CapturingInput()
	}
	return false
}

// renderTitle paints the app-wide title bar shown at the very top of every
// screen. It uses Width(m.width) so the colored background spans the full
// terminal regardless of label length. When dry-run mode is on a yellow
// [DRY-RUN] suffix is appended so the user can never lose track of the
// mode.
func (m Model) renderTitle() string {
	style := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("57")).
		Align(lipgloss.Center)
	if m.width > 0 {
		style = style.Width(m.width)
	}
	label := appTitle()
	if audit.IsDryRun() {
		badge := lipgloss.NewStyle().
			Foreground(lipgloss.Color("214")).
			Bold(true).
			Render(" [DRY-RUN]")
		label = appTitle() + badge
	}
	return style.Render(label)
}

// resetToProfilePicker clears the stack back to just the profile picker.
func (m Model) resetToProfilePicker() (Model, tea.Cmd) {
	picker := m.sizeView(profile.New(m.state))
	m.stack = []tea.Model{picker}
	return m, picker.Init()
}

// resetToRegionPicker clears the stack back to [profile, region]. Only safe
// to call when awsCtx is set (a profile has already been selected).
func (m Model) resetToRegionPicker() (Model, tea.Cmd) {
	picker := m.sizeView(profile.New(m.state))
	rpicker := m.sizeView(region.New(m.awsCtx, m.state))
	m.stack = []tea.Model{picker, rpicker}
	return m, rpicker.Init()
}

// sizeView delivers the current WindowSizeMsg to a freshly constructed view
// so its first View() call can use real dimensions. Bubble Tea does not
// re-broadcast WindowSizeMsg on push, so views pushed after the program has
// started would otherwise render at 0x0 until the next terminal resize.
// Subtract two rows so the view fits alongside the title bar (top) and
// the status footer (bottom).
func (m Model) sizeView(v tea.Model) tea.Model {
	if m.width <= 0 || m.height <= 0 {
		return v
	}
	updated, _ := v.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height - 2})
	return updated
}
