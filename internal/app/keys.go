package app

// Global keybindings. Views handle their own local keys (j/k navigation,
// enter, esc, etc.) without conflicting with these.
//
// | Key      | Action                                    |
// |----------|-------------------------------------------|
// | ctrl+c   | Quit                                      |
// | ctrl+l   | Lock (wipe unlock marker) and quit        |
// | ctrl+p   | Jump to profile picker                    |
// | ctrl+r   | Jump to region picker (if context exists) |
// | ?        | Toggle help overlay                       |
//
// esc is intentionally view-local: each view decides whether to clear a
// filter, back out of a sub-mode, or pop the view stack via nav.PopView().
const (
	keyQuit          = "ctrl+c"
	keyLock          = "ctrl+l"
	keyProfilePicker = "ctrl+p"
	keyRegionPicker  = "ctrl+r"
	keyToggleHelp    = "?"
)
