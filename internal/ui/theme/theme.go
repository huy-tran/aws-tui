// Package theme is a tiny palette layer that flips the colour roles whose
// dark-default values look bad on light terminals (status bar background,
// table cell borders, muted text). Semantic colours - red errors, yellow
// warnings, green healthy, magenta cursor highlight - are unchanged
// because they map to ANSI positions that work on both backgrounds.
package theme

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette is the small set of colour roles that materially shift between
// dark and light terminals. Everything else in the app stays on its
// per-package literal colour.
type Palette struct {
	StatusBg lipgloss.Color
	StatusFg lipgloss.Color
	StatusContextFg lipgloss.Color
	BorderFg lipgloss.Color
	MutedFg  lipgloss.Color
}

var (
	// Dark is the default palette tuned for dark terminals.
	Dark = Palette{
		StatusBg:        lipgloss.Color("237"),
		StatusFg:        lipgloss.Color("252"),
		StatusContextFg: lipgloss.Color("252"),
		BorderFg:        lipgloss.Color("240"),
		MutedFg:         lipgloss.Color("241"),
	}
	// Light is the inverted palette for light terminals: lighter
	// status-bar background so the bottom row doesn't read as a black
	// strip, darker text on it, and a higher-contrast border / muted.
	Light = Palette{
		StatusBg:        lipgloss.Color("254"),
		StatusFg:        lipgloss.Color("235"),
		StatusContextFg: lipgloss.Color("236"),
		BorderFg:        lipgloss.Color("247"),
		MutedFg:         lipgloss.Color("244"),
	}
)

var current = Dark

// Set installs the active palette. Safe to call once at startup.
func Set(p Palette) { current = p }

// Current returns the active palette. Cheap to call inside style builders.
func Current() Palette { return current }

// SetByName accepts "dark" / "light" / "auto" and installs the matching
// palette. "auto" uses lipgloss' terminal background probe. Returns an
// error for anything else.
func SetByName(name string) error {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "auto":
		if lipgloss.HasDarkBackground() {
			current = Dark
		} else {
			current = Light
		}
		return nil
	case "dark":
		current = Dark
		return nil
	case "light":
		current = Light
		return nil
	}
	return fmt.Errorf("unknown theme %q (want dark|light|auto)", name)
}
