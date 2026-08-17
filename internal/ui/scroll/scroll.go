// Package scroll keeps viewport scrolling in step with datatable scrolling.
//
// The two components come from different places - datatable is ours, viewport
// is bubbles - and their defaults disagree in two ways:
//
//	          | top / bottom | page up      | page down
//	datatable | g / G        | ctrl+b       | ctrl+f
//	viewport  | (none)       | b            | f
//
// So "jump to the bottom" worked on every list but on no scrolling screen,
// and ctrl+b/ctrl+f died silently in viewports. KeyMap and Jump close both
// gaps without removing the pager-style keys viewport users expect.
package scroll

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
)

// KeyMap returns the viewport defaults plus the ctrl+b / ctrl+f paging that
// datatable uses. The stock b / f / pgup / pgdown / space keys still work.
func KeyMap() viewport.KeyMap {
	km := viewport.DefaultKeyMap()
	km.PageUp = key.NewBinding(
		key.WithKeys("pgup", "b", "ctrl+b"),
		key.WithHelp("b/pgup", "page up"),
	)
	km.PageDown = key.NewBinding(
		key.WithKeys("pgdown", " ", "f", "ctrl+f"),
		key.WithHelp("f/pgdn", "page down"),
	)
	return km
}

// Jump handles the top/bottom keys that viewport has no binding for. It
// reports whether it consumed the key, so callers can fall through to
// viewport.Update for everything else.
//
// The key strings match datatable: "home"/"g" to the top, "end"/"G" to the
// bottom.
func Jump(vp *viewport.Model, k string) bool {
	switch k {
	case "home", "g":
		vp.GotoTop()
		return true
	case "end", "G":
		vp.GotoBottom()
		return true
	}
	return false
}
