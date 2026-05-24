# 17 — Status Footer

A persistent one-line bar pinned to the bottom of every screen so the user always knows:

- Which profile and region they're operating against (mirrored from the dashboard breadcrumb, but visible from pushed sub-views too).
- How fresh the data on screen is — the last successful load timestamp and the relative age ("12s ago" / "4m ago").
- Where they are in the data — current item count visible in the active table.
- The most recent action / status message (yank confirmations, save acknowledgements, error tails) for two beats before they fade back to the freshness summary.

## Layout

```
prod-x · ap-southeast-2 · EC2  ·  47 items  ·  loaded 12s ago
```

When something just happened the action half displaces the freshness line:

```
prod-x · ap-southeast-2 · EC2  ·  47 items  ·  copied bucket name
```

Style: a muted single-row strip below the existing view body (and above the title bar at the very top stays). Same colour palette as the dashboard's existing footer text so it doesn't fight the data.

## What goes where

| Segment       | Source                                                                                |
|---------------|---------------------------------------------------------------------------------------|
| profile       | `aws.Context.Profile`                                                                 |
| region        | `aws.Context.Region`                                                                  |
| view name     | dashboard's `tabNames[active]`, or the pushed view's own label                        |
| item count    | active view's `StatusFooter()` reports it (typically `len(displayedRows)`)            |
| freshness     | active view's `StatusFooter()` reports a `LastLoaded time.Time`                       |
| transient msg | active view's `StatusFooter()` `Message` field                                         |

## Implementation

Lives at the **app** layer like the title bar so pushed views inherit it. New package `internal/ui/statusbar`:

```go
package statusbar

type Snapshot struct {
    Profile    string
    Region     string
    View       string     // "EC2", "Parameter Store · value", etc.
    Items      int        // -1 = don't show
    LastLoaded time.Time  // zero = don't show
    Message    string     // recent action; if set, replaces the freshness segment
}

func Render(s Snapshot, width int) string { ... }
```

Views opt in by implementing `Reporter`:

```go
package statusbar

type Reporter interface {
    StatusFooter() Snapshot
}
```

The app:
- Pulls a `Snapshot` from the top view via the interface (zero value if not implemented).
- Fills in Profile / Region / View name itself, since the view doesn't necessarily know its profile (and shouldn't have to).
- Renders one row, full width, below the body.

## Sizing

The title takes one row at the top; the status bar takes one at the bottom. WindowSizeMsg forwarded to views must subtract **two** rows now (currently subtracts one for the title). `sizeView` likewise.

## Freshness clock

The freshness segment ("12s ago") needs to tick over time. Two options:

1. **Compute on render.** The view re-renders on every Bubble Tea cycle, so as long as the user touches the keyboard occasionally the timestamp updates naturally. Stale during idle but cheap.
2. **Tick a 1s timer.** Forces the view to re-render even when idle.

Pick #1. Idle staleness is acceptable; firing a 1s timer everywhere is more code than the feature warrants. When the user does anything (move cursor, refresh) the timestamp updates.

## Message decay

Action messages ("copied bucket name", "saved /app-prod/foo (v8)") are typically already set in `m.status` by the view. They live until the view explicitly clears them (after the next keypress on most views). The status bar just reads whatever the view currently reports - no extra fade timer.

## Acceptance criteria

- Bottom row of every screen shows `profile · region · view`, plus item count and freshness when the view reports them.
- When the view sets a status message (yank confirm, save success, error tail), the message replaces the freshness segment until cleared.
- Title bar still sits at the very top; status bar sits at the very bottom; view content fills the middle with no overflow.
- Views without `StatusFooter()` still get the profile / region / view-name segment (the rest is just omitted gracefully).
- Resizing the terminal lays the bar out correctly at the new width.
