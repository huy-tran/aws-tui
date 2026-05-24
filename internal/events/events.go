// Package events is a tiny singleton bridge from background goroutines
// (live tail pump, future websocket consumers) back into the Bubble Tea
// program loop. The runtime sets the program reference once at startup;
// any goroutine can then call events.Send(msg) to deliver a tea.Msg to
// the active program from outside the normal Cmd flow.
package events

import (
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

var (
	mu   sync.RWMutex
	prog *tea.Program
)

// SetProgram installs the program reference. Safe to call once at startup
// before any tea.NewProgram().Run().
func SetProgram(p *tea.Program) {
	mu.Lock()
	prog = p
	mu.Unlock()
}

// Send forwards msg to the installed program. Safe to call from any
// goroutine. A no-op when no program is installed yet (early startup
// races degrade silently).
func Send(msg tea.Msg) {
	mu.RLock()
	p := prog
	mu.RUnlock()
	if p != nil {
		p.Send(msg)
	}
}
