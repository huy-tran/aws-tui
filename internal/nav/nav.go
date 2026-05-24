// Package nav holds the message types that views emit to drive navigation
// in the root app model. It lives outside internal/app so that view
// packages can import it without creating an app↔view import cycle.
package nav

import tea "github.com/charmbracelet/bubbletea"

// PushViewMsg pushes a new view onto the stack.
type PushViewMsg struct {
	View tea.Model
}

// PopViewMsg pops the current view.
type PopViewMsg struct{}

// ProfileSelectedMsg is emitted when the user picks a profile.
type ProfileSelectedMsg struct {
	Profile string
}

// RegionSelectedMsg is emitted when the user picks a region.
type RegionSelectedMsg struct {
	Region string
}

func PushView(v tea.Model) tea.Cmd {
	return func() tea.Msg { return PushViewMsg{View: v} }
}

func PopView() tea.Cmd {
	return func() tea.Msg { return PopViewMsg{} }
}
