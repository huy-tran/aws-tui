// Package loader is a small wrapper around bubbles/spinner that the views
// use to render their "loading..." states with a live animation. Each view
// owns one loader and drives it whenever any of its load flags are set.
//
// Usage pattern:
//
//	m.loader = loader.New()
//	return tea.Batch(m.loader.Tick(), m.loadCmd())   // from Init / on (re)load
//
//	case spinner.TickMsg:
//	    if !m.isLoading() { return m, nil }
//	    var cmd tea.Cmd
//	    m.loader, cmd = m.loader.Update(msg)
//	    return m, cmd
//
//	// in View:
//	if m.loading { return m.loader.Render("loading X...") }
//
// Dropping the cmd returned by Update when nothing is loading lets the
// animation stop cleanly; calling Tick() again on the next load restarts
// it.
package loader

import (
	"fmt"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

type Model struct {
	spinner spinner.Model
}

func New() Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return Model{spinner: s}
}

// Tick returns the command that fires the first / next spinner.TickMsg.
// Call this from Init() and any time a fresh load begins.
func (l Model) Tick() tea.Cmd { return l.spinner.Tick }

// Update advances the spinner animation. The returned cmd schedules the
// next tick - drop it when nothing is loading to halt the animation.
func (l Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	l.spinner, cmd = l.spinner.Update(msg)
	return l, cmd
}

// Render returns the spinner glyph followed by the supplied label, or just
// the glyph when label is empty.
func (l Model) Render(label string) string {
	if label == "" {
		return l.spinner.View()
	}
	return fmt.Sprintf("%s %s", l.spinner.View(), label)
}
