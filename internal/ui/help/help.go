// Package help renders the centered keybinding overlay shown when the user
// presses '?'. Views opt in by implementing Provider; the app composes
// their sections with the always-present global section.
package help

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Item is one keybinding row in a help section.
type Item struct {
	Keys string
	Desc string
}

// Section is a labeled group of keybindings (e.g. "EC2 list", "Beanstalk details").
type Section struct {
	Title string
	Items []Item
}

// Provider is implemented by views that contribute keybindings to the
// overlay. Most views will tailor the returned sections to their current
// mode (filter on / off, sub-screen, etc).
type Provider interface {
	HelpItems() []Section
}

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	keyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	descStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	mutedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	boxStyle     = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("213")).
			Padding(1, 2)
)

// Render builds the overlay body for the given sections. The caller is
// responsible for centering / placing the returned string.
func Render(sections []Section) string {
	// Right-pad keys so descriptions line up.
	keyWidth := 0
	for _, sec := range sections {
		for _, it := range sec.Items {
			if w := lipgloss.Width(it.Keys); w > keyWidth {
				keyWidth = w
			}
		}
	}

	var lines []string
	lines = append(lines, titleStyle.Render("Help"))
	for _, sec := range sections {
		lines = append(lines, "")
		lines = append(lines, sectionStyle.Render(sec.Title))
		for _, it := range sec.Items {
			pad := strings.Repeat(" ", keyWidth-lipgloss.Width(it.Keys))
			lines = append(lines, fmt.Sprintf("  %s%s  %s",
				keyStyle.Render(it.Keys), pad, descStyle.Render(it.Desc)))
		}
	}
	lines = append(lines, "", mutedStyle.Render("  esc or ? to close"))
	return boxStyle.Render(strings.Join(lines, "\n"))
}

// GlobalSection returns the always-present keybindings.
func GlobalSection() Section {
	return Section{
		Title: "Global",
		Items: []Item{
			{Keys: "ctrl+c", Desc: "quit"},
			{Keys: "ctrl+l", Desc: "lock now (wipe TOTP unlock, quit)"},
			{Keys: "ctrl+p", Desc: "switch profile"},
			{Keys: "ctrl+r", Desc: "switch region"},
			{Keys: "tab / shift+tab", Desc: "next / previous tab"},
			{Keys: "←/→", Desc: "next / previous tab (root list)"},
			{Keys: "s then 1..N", Desc: "sort active table by column (repeat to flip direction)"},
			{Keys: "b", Desc: "bookmark current row (toggle)"},
			{Keys: "B", Desc: "open bookmarks list"},
			{Keys: "?", Desc: "toggle this help"},
		},
	}
}
