# UI Conventions — AWS TUI

Build terminal-UI screens that match the "AWS TUI" house style. This is a
Go Bubble Tea app using charmbracelet/lipgloss for styling and
lipgloss/table for tables. Follow the rules below exactly. All colors are
ANSI 256-color codes passed to `lipgloss.Color("…")`.

## App name & title bar

- The app is named **"AWS TUI"**. Render the title as `AWS TUI - <version>`
  when a tagged build version is present, otherwise just `AWS TUI`
  (never show "AWS TUI - vdev" for dev builds).
- A persistent title bar sits at the very top of EVERY screen:
  - Bold, centered, spanning the full terminal width (`Width(m.width)` so the
    colored background fills the row regardless of label length).
  - Foreground `231` (near-white) on background `57` (indigo/purple).
- When the app is in dry-run mode, append a bold ` [DRY-RUN]` badge in
  foreground `214` (amber) so the mode is always visible.
- Layout is three stacked regions, joined top-to-bottom:
  `title bar` (1 row) / `body` (the active view) / `status footer` (1 row),
  with a blank spacer row between the title and the body and another between
  the body and the footer so the regions don't sit flush against each other.
  Reserve four rows total for the chrome (title + footer + two spacers) when
  sizing views.

## Status footer

A single bottom row reporting context and freshness. Views report a
`Snapshot{ Profile, Region, View, Items, LastLoaded, Message }`.

- Bar style: background + foreground from the active theme palette
  (`StatusBg`/`StatusFg`), padded `0,1`.
- Build the body from these segments, joined by a muted `  ·  ` separator:
  1. Context — `profile · region · view`, bold, in `StatusContextFg`.
  2. Item count — `"N items"`, muted (`MutedFg`). Omit when count < 0.
  3. Either a transient action message (foreground `214`, bold) OR, when no
     message, a freshness string `"loaded <ago>"` in muted text.
- Freshness uses short relative units: `12s ago`, `4m ago`, `2h ago`, `3d ago`.

## Tables (the datatable component)

Tables are the primary content surface. Use a bordered, scrollable table that
owns its own cursor + viewport offset.

- **Borders:** `lipgloss.NormalBorder()` with BOTH `BorderRow(true)` and
  `BorderColumn(true)` — every cell is boxed. Border foreground comes from the
  theme (`BorderFg`), NOT a literal.
- **Header row:** bold, foreground `252`.
- **Active (cursor) row:** highlighted with a subtle background BAND only —
  background `235`, no foreground change. This overlays on top of any per-cell
  foreground coloring (e.g. folder/selected markers) without clobbering it.
  Never highlight the cursor by mutating the underlying row data; do it with a
  StyleFunc at render time.
- **Columns** have a Title, a soft-minimum Width (0 = derive from content), an
  optional Flex flag (one column absorbs leftover horizontal width; defaults to
  column 0), an Align, and a SortAs kind.
- **Sorting:**
  - Sort kinds: `String` (case-insensitive lexicographic, default),
    `Numeric` (compares leading numeric prefix, e.g. "5d", "1.2 G"),
    `Time` (parses `2006-01-02 15:04:05`).
  - Press `s` to raise a sort ribbon, then a digit `1..N` or a column's first
    letter to pick the column. Re-selecting the active column flips direction.
  - The active sort column shows a ` ↑` / ` ↓` arrow in its header.
  - Sort ribbon style: prompt + column list in foreground `214` bold, with a
    muted `(esc to cancel)` hint in `241`.
  - Sorting preserves the cursor's logical row across reorders (re-find it by
    value after sorting).
- **Truncation:** measure VISIBLE width (`lipgloss.Width`), not rune count, so
  ANSI-styled cells aren't mis-cut. When a styled cell must be shortened, strip
  the styling and append `…` rather than risk slicing inside an escape.
- **Navigation keys:** `up/k`, `down/j`, `pgup/ctrl+b`, `pgdown/ctrl+f/space`,
  `home/g` (top), `end/G` (bottom).

### Detail panels (key/value tables)

Detail screens that show a single record's attributes (EC2 instance details,
Beanstalk / RDS / CodeDeploy / SecurityHub detail, Parameter Store value
metadata) render as a static two-column `Field` / `Value` table via
`datatable.RenderKeyValue`, NOT as a list of `label: value` text lines. This
keeps detail panels visually consistent with the scrollable lists.

- Same borders and header styling as the scrollable table, but no cursor, no
  sort and no navigation - it's a static grid.
- The `Value` column is content-sized; pass the view width as `maxWidth` so a
  long value (e.g. a CNAME or ARN) is truncated to fit rather than wrapping.
- Use meaningful column titles when the pairs aren't generic attributes (e.g.
  `Tag` / `Value` for an EC2 instance's tags).
- Styled values (status badges, health dots, severity) pass straight through
  as cell content.

## Help overlay

Press `?` for a centered overlay of keybindings.

- Box: `RoundedBorder()`, border foreground `213` (magenta/pink), padded `1,2`.
- "Help" title bold in `213`. Section headers bold in `252`. Keys in `214`,
  descriptions in `252`. Right-pad keys so descriptions align in a column.
- Footer hint `esc or ? to close` in muted `241`.
- A "Global" section is always present (quit, switch profile/region, tab
  navigation, sort, bookmark, toggle help); views append their own sections.

## Loading state

- Use a `bubbles/spinner` with the `Dot` spinner.
- Render as `<spinner> loading <thing>...`.
- Drop the spinner's tick command when nothing is loading so the animation
  stops cleanly; restart with a fresh Tick() on the next load.

## Color palette & theming

Two palettes — Dark (default) and Light — and only the roles that genuinely
shift between dark/light terminals are themed. Everything else uses a literal
per-component color. Theme is selected via `--theme=dark|light|auto` or
`AWS_TUI_THEME`; `auto` probes the terminal background.

Themed roles:

| Role            | Dark | Light |
|-----------------|------|-------|
| StatusBg        | 237  | 254   |
| StatusFg        | 252  | 235   |
| StatusContextFg | 252  | 236   |
| BorderFg        | 240  | 247   |
| MutedFg         | 241  | 244   |

Fixed literal/semantic colors (the same on both backgrounds):

- Title bar: fg `231` on bg `57`.
- Header text / primary text: `252`.
- Active-row band: bg `235`.
- Accent / action / keys / sort-prompt: `214` (amber).
- Overlay border & headings / cursor-highlight role: `213` (magenta).
- Errors: red. Warnings: yellow/`214`. Healthy: green. These map to ANSI
  positions that read correctly on both light and dark terminals — do not
  theme them.

When in doubt: theme only the five roles in the table above; use the literal
semantic colors for everything else; never hardcode the status-bar or border
colors.
