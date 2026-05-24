# 25 — Bookmarks

Frequent-access for the same handful of resources. Press `b` on a row in any tab's root list to bookmark it; press `B` from any root list to open the bookmarks list. Per-profile (so prod-x bookmarks don't pollute dev-x). Persisted in the existing `state.json`.

## Layout — bookmarks list

```
┌─ Bookmarks (4) ───────────────────────────────────────────────────┐
│   Tab               Region            Label                        │
│ > Parameter Store   ap-southeast-2    /app-prod/db/password        │
│   EC2               ap-southeast-2    web-prod-01 (i-0abc...)      │
│   Beanstalk         ap-southeast-2    api / api-prod               │
│   CloudFront        ap-southeast-2    d.example.com (E1XYZ)        │
│                                                                    │
│ enter: open · d: delete · esc: back                                │
└────────────────────────────────────────────────────────────────────┘
```

`enter` jumps to that tab and pre-fills its filter with the bookmark's ID (so the user lands looking at exactly the bookmarked item). `d` deletes the bookmark. `esc` pops back to whatever the user was on.

## Interaction

- `b` on a root list row: capture the current selection, save bookmark, show "bookmarked X" in the status footer. Idempotent — pressing `b` again on an already-bookmarked row removes it. (Optional polish; can also be a one-way add with `d` in the bookmarks list as the only delete path.)
- `B` from any root list: push the bookmarks list view.
- `b` inside a sub-mode (filter open, sub-screen, edit form): falls through to the view. Adding from a sub-screen is out of scope; come back to root first.

## Data model

```go
package state

type Bookmark struct {
    Tab     string `json:"tab"`    // "EC2" / "Parameter Store" / ...
    Region  string `json:"region"`
    ID      string `json:"id"`     // resource identifier (used as filter query when reopened)
    Label   string `json:"label"`  // human-friendly text shown in the list
    AddedAt string `json:"added_at"` // RFC3339
}

type Store struct {
    ...
    Bookmarks map[string][]Bookmark `json:"bookmarks"` // profile -> bookmarks
}

func (s *Store) AddBookmark(profile string, b Bookmark)
func (s *Store) RemoveBookmark(profile, tab, id string) bool
func (s *Store) ListBookmarks(profile string) []Bookmark
```

## Wiring

Each tab implements two small interfaces consumed by the dashboard:

```go
// Bookmarkable returns the bookmark for the currently-highlighted row.
// ok=false means "nothing to bookmark right now" (loading, empty, etc).
type Bookmarkable interface {
    BookmarkCurrent() (label, id string, ok bool)
}

// FilterTarget lets the dashboard pre-fill a tab's filter when the user
// chooses a bookmark on that tab. Optional - tabs without a filter
// just don't implement it.
type FilterTarget interface {
    SetFilterQuery(q string)
}
```

The dashboard handles `b` / `B` at its own layer (gated by `!CapturingInput && !InSubnav`); it calls `BookmarkCurrent` on the active tab and saves via the store. When the bookmarks list view emits a chosen-bookmark message, the dashboard switches active tab by name lookup and calls `SetFilterQuery` if present.

## Navigation back from the bookmarks list

The bookmarks view is pushed onto the app stack (mirroring EC2 details / S3 browser / etc). Pressing `enter` returns a `BookmarkChosenMsg` and the app:

1. Pops the bookmarks view off the stack.
2. Forwards the message to the dashboard (now at the top).
3. Dashboard sets `active` to the chosen tab and (if available) `SetFilterQuery(id)`.

This keeps the bookmarks view independent of dashboard internals.

## Acceptance criteria

- `b` on a root list adds a bookmark for the current row; pressing `b` again removes it.
- `B` from any root list opens the bookmarks pane.
- Bookmarks persist in `state.json` under `bookmarks[<profile>]`.
- `enter` on a bookmark jumps to that tab and pre-fills its filter with the bookmark ID.
- `d` on a bookmark removes it.
- Switching profile shows only that profile's bookmarks.
- All filesystem writes go through the existing atomic-write path in `state.Store.Save()`.
- No bookmark stored contains sensitive payload (only id + label) — Parameter Store bookmarks store the name, never the value.
