# 22 — Unit Tests Across Views

Today only `dashboard`, `datatable`, and `aws/cache` have unit tests. Every view's `Update` is testable with stub tea.Msg values without AWS — we just haven't done it. This spec sets the minimum bar so future refactors have a safety net.

## What to cover

Per view, the behaviours that matter:

- **Filter on/off transitions.** `/` enters filter, esc clears, enter exits while keeping the query, typing accumulates into `m.filter`.
- **Filter narrowing.** Setting rows + a filter query results in the expected `displayed` subset.
- **Cursor + selection.** `up/down` move cursor in the underlying table; the view's `selected*()` returns the right record.
- **Yank menu state machines.** S3 / RDS / SecurityHub: pressing `y` sets `yankPending`; the next key triggers the right clipboard write or muted message.
- **Mode transitions.** Beanstalk modeList -> modeDetails on enter; CloudFront modeList -> modeInvalidations on `v`; Paramstore modeList -> modeValue on enter; etc. Just confirm the mode flips without asserting on rendered output.
- **CapturingInput / InSubnav interface contracts.** Verifying the values for each mode means future changes to the dashboard's routing don't silently break.

## What NOT to cover

- View rendering output (would couple tests to the cell-border / colour palette). The `datatable` package already tests its own rendering.
- AWS SDK calls. The load commands aren't easily faked without an interface boundary; reproduce them in integration tests later. Unit tests focus on what happens after a `loadedMsg` arrives.
- Time-dependent things (status footer freshness). Already covered by the `time` field being formatted on render.

## Patterns

Tests construct a Model via `New(ctx)` with a nil-or-stub `aws.Context` (most paths never touch it during Update). They then feed `tea.KeyMsg` / typed-msg values to `Update` and inspect the returned model's fields.

Example skeleton (EC2):

```go
func TestEC2FilterTransitions(t *testing.T) {
    m := New(testContext())
    if m.CapturingInput() { t.Fatal("starts not capturing") }

    m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")}).(Model), nil
    if !m.CapturingInput() { t.Fatal("/ should open filter") }

    m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc}).(Model), nil
    if m.CapturingInput() { t.Fatal("esc should close filter") }
}
```

## Acceptance criteria

- Each tab view package has a `*_test.go` covering the filter on/off transition, at minimum.
- `go test ./...` runs in under 5 seconds total.
- No test touches `~/.aws-tui/` (use `t.TempDir()` if persistence is required).
- No test panics or hits the network.
