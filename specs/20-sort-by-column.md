# 20 — Sort by Column

The `datatable` widget renders rows in whatever order the view passes them. Press `s` to open a "sort by which column?" prompt, the column letter to select, and the table re-sorts. Press `s` again on the same column to flip direction. The sort persists for the lifetime of the view.

## Interaction

1. User presses `s` on a table-bearing view (any tab).
2. A small ribbon appears below the column headers showing each column's index/letter highlighted:
   ```
   sort by: [1]Name  [2]Instance ID  [3]State  [4]Type  [5]Priv IP   (esc to cancel)
   ```
3. User types `1` (or `n` for Name, `i` for Instance ID, etc — first-letter-of-title shortcut), the widget sorts ascending on that column. The ribbon disappears.
4. Pressing `s` then the same column letter toggles direction (asc → desc → asc).
5. A small indicator next to the active sort column's header — `↑` ascending, `↓` descending — shows the current sort.

## Data model

`datatable.Model` gains:

```go
sortCol int  // -1 = no sort
sortDir int  // +1 ascending, -1 descending
```

Sorting is **lexicographic on the displayed (post-truncate) string** by default. That's good enough for "Name", "Title", etc. For numeric / date columns the view can tag a column with `SortAs`:

```go
type Column struct {
    Title  string
    Width  int
    Flex   bool
    Align  lipgloss.Position
    SortAs SortKind  // 0 = String, 1 = Numeric, 2 = Time
}
```

`Numeric` parses leading digits (handles "423 matches" → 423; "5d" → 5d as a duration would, but for v1 just trim trailing units). `Time` parses the canonical "2006-01-02 15:04:05" format that the views use for `formatTime`. Anything unparseable falls back to string compare so a misconfigured column never crashes.

The sort runs on the actual `m.rows` slice (post `SetRows`), not on the upstream view's data. Cursor / selected-row stays on the same row by value after a sort (we remember the previous selection's row pointer and re-locate it).

## Mode interaction

The "pick a column" ribbon is a transient mode inside the widget. It implements `CapturingInput()` by returning true while open so the dashboard's letter hotkeys (currently none, but future-proof) don't intercept the digit/letter keypress.

If a view already binds `s` (CloudWatch uses `s` for search), the view takes precedence — it just doesn't propagate `s` down to the datatable in that case. Currently only CloudWatch binds it; everything else is free to use `s` for sort.

## Persistence

Sort state is per-view, not persisted across runs. (Could be added later via the same `state.json`; out of scope for v1 — sorting is fast enough that the user can re-pick after a restart.)

## Acceptance criteria

- Pressing `s` on any sortable view shows the column ribbon.
- Pressing a column digit (1-N) or first-letter-of-title sorts the table; ribbon disappears.
- Pressing `s` again then the same column toggles ascending/descending.
- Active sort column shows ↑/↓ in its header.
- Cursor stays anchored to the same logical row across a sort.
- Esc cancels the ribbon without changing sort.
- Views that bind `s` for something else (CloudWatch search) are unaffected.
- Numeric / time columns sort by parsed value, not lexicographically.
