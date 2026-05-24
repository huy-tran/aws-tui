// Package statusbar renders the persistent bottom-of-screen row that
// shows the active profile / region / view, the visible item count,
// data freshness, and the most recent action message.
package statusbar

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/huy-tran/aws-tui/internal/ui/theme"
)

// Snapshot is the data a view reports to the status bar.
type Snapshot struct {
	Profile    string
	Region     string
	View       string
	Items      int       // -1 = omit
	LastLoaded time.Time // zero = omit
	Message    string    // when non-empty, replaces the freshness segment
}

// Reporter is the optional interface views implement to populate the bar.
type Reporter interface {
	StatusFooter() Snapshot
}

var msgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)

func barStyle() lipgloss.Style {
	p := theme.Current()
	return lipgloss.NewStyle().
		Background(p.StatusBg).
		Foreground(p.StatusFg).
		Padding(0, 1)
}

func contextStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusContextFg).Bold(true)
}

func dimStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().MutedFg)
}

// Render returns the bar at the given total width. width <= 0 returns the
// content without a width constraint - lipgloss will still render it.
func Render(s Snapshot, width int) string {
	var segments []string
	ctxParts := []string{}
	if s.Profile != "" {
		ctxParts = append(ctxParts, s.Profile)
	}
	if s.Region != "" {
		ctxParts = append(ctxParts, s.Region)
	}
	if s.View != "" {
		ctxParts = append(ctxParts, s.View)
	}
	if len(ctxParts) > 0 {
		segments = append(segments, contextStyle().Render(strings.Join(ctxParts, " · ")))
	}
	if s.Items >= 0 {
		segments = append(segments, dimStyle().Render(fmt.Sprintf("%d items", s.Items)))
	}
	if s.Message != "" {
		segments = append(segments, msgStyle.Render(s.Message))
	} else if !s.LastLoaded.IsZero() {
		segments = append(segments, dimStyle().Render("loaded "+humanAgo(time.Since(s.LastLoaded))))
	}
	body := strings.Join(segments, dimStyle().Render("  ·  "))
	style := barStyle()
	if width > 0 {
		style = style.Width(width)
	}
	return style.Render(body)
}

// humanAgo formats a duration as a short "12s ago" / "4m ago" / "2h ago"
// / "3d ago" string suitable for the freshness segment.
func humanAgo(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
