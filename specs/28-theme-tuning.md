# 28 — Theme Tuning

aws-tui's colours were tuned for dark terminals. On a light terminal the status bar's dark-grey background turns the bottom row into a black bar, the table cell borders blend into the page, and the muted "press / to filter" hint is hard to read.

This spec introduces a minimal theme layer covering the surfaces that materially shift between light and dark, plus a `--theme=light|dark|auto` override.

## Scope

Only the surfaces that look genuinely bad in the wrong terminal background get themed:

- **Status bar** (`internal/ui/statusbar`) - background + foreground swap.
- **Table cell borders** (`internal/ui/datatable`) - lighter grey on light terminals.
- **Muted text** (the "press / to filter" hints) - one shade darker on light terminals.

Semantic colours (red for errors, green for healthy, yellow for warnings, the magenta cursor highlight) stay put: they map to ANSI colour-cube positions that look correct on both backgrounds.

## Detection

`lipgloss.HasDarkBackground()` queries the terminal via OSC11 and returns a bool. It's not 100% reliable across every terminal but it's good enough for the default. Users on terminals that don't reply correctly can force the right theme via flag/env.

## Override

- `--theme=light|dark|auto` (default `auto`)
- `AWS_TUI_THEME=light|dark|auto`

Flag wins over env. `auto` defers to `lipgloss.HasDarkBackground()`. Anything else is invalid -> stderr + exit 2.

## Implementation

New package `internal/ui/theme`:

```go
package theme

type Palette struct {
    StatusBg, StatusFg   lipgloss.Color
    BorderFg             lipgloss.Color
    MutedFg              lipgloss.Color
}

var (
    Dark  = Palette{ StatusBg: "237", StatusFg: "252", BorderFg: "240", MutedFg: "241" }
    Light = Palette{ StatusBg: "254", StatusFg: "235", BorderFg: "247", MutedFg: "244" }
)

var current = Dark

func Set(p Palette)              { current = p }
func Current() Palette            { return current }
func SetByName(name string) error { ... }
```

`cmd/aws-tui` parses the flag/env once before launching the Bubble Tea program and calls `theme.Set(...)`. The themed packages consult `theme.Current()` in their styles.

Datatable + statusbar update their style construction to read from `theme.Current()` instead of hardcoded colour strings.

## Not in scope (yet)

- Custom-palette config file. A real theme system would let users tweak individual colours via `~/.aws-tui/theme.toml`. Out of scope for v1.
- Re-themable badge / status colours. Keep red/yellow/green semantic; users on really weird terminals can run in `dark` mode and accept the trade-off.
- Live theme reload (watch terminal bg change). Detect once at startup, period.

## Acceptance criteria

- `--theme=dark` and `--theme=light` switch the status bar, table border, and muted-text colours.
- `--theme=auto` (default) follows `lipgloss.HasDarkBackground()`.
- `AWS_TUI_THEME` env is honoured when no flag is set.
- Bad theme name -> stderr error + exit 2.
- Semantic colours (red errors / yellow warnings / green healthy / magenta cursor) are unchanged.
- Existing tests still pass.
