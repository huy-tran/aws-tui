# 27 — In-TUI Log Tail (StartLiveTail)

Today the CloudWatch tab's `t` key shells out to `aws logs tail --follow` via `tea.ExecProcess`. The TUI suspends, the user gets the streaming view, hits `Ctrl+C` to return. It works but the seam is jarring: no filter / sort / yank inside the tail, and the surrounding tab context is gone.

This spec replaces the shell-out with an in-TUI streaming view using the CloudWatch Logs `StartLiveTail` API.

## What `StartLiveTail` gives us

A single bidirectional event stream over HTTP/2. Server pushes one event per matched log entry; payload is `{timestamp, logStreamName, message}`. Stream stays open until the client closes or the server times out (~1h limit, then needs reconnect).

The SDK exposes this as `cloudwatchlogs.StartLiveTail(ctx, input)` returning a `*StartLiveTailEventStream` with a `.Events()` channel.

## Architecture

A goroutine owns the SDK call and the result channel; it forwards each event into Bubble Tea via the standard "send a tea.Msg" pattern:

```go
// in modeTail, when entering:
prog := tea.NewProgram(...) // already exists
go pumpLiveTail(ctx, logGroupARN, prog.Send)

// pumpLiveTail loop:
for ev := range stream.Events() {
    switch e := ev.(type) {
    case *types.StartLiveTailResponseStreamMemberSessionUpdate:
        for _, entry := range e.Value.SessionResults {
            send(liveTailLineMsg{...})
        }
    case *types.StartLiveTailResponseStreamMemberSessionStart:
        send(liveTailStartedMsg{})
    }
}
send(liveTailEndedMsg{err: stream.Err()})
```

The Bubble Tea side receives `liveTailLineMsg` per line and appends to a ring buffer rendered in a viewport.

## Reaching the Program from a non-tea goroutine

Bubble Tea offers `tea.Program.Send(tea.Msg)` for exactly this. The cleanest pattern is to capture the program reference in `cmd/aws-tui/main.go` and expose it via a small bus:

```go
package events

var prog *tea.Program

func SetProgram(p *tea.Program) { prog = p }

func Send(msg tea.Msg) {
    if prog != nil { prog.Send(msg) }
}
```

Production-quality alternative would be passing the bus down via context; for v1 the singleton matches how `audit` / `auth` work already.

## Layout — tail running

```
┌─ Tail: /aws/lambda/image-processor ───────────────────────────────┐
│ live · 47 lines · started 00:32                                    │
│                                                                    │
│ 14:32:01 [i-0abc] INFO  processed image 12345                      │
│ 14:32:03 [i-0abc] WARN  retry 1/3 for image 12346                  │
│ 14:32:04 [i-0abc] INFO  processed image 12346                      │
│ ...                                                                │
│                                                                    │
│ /: filter (regex) · p: pause/resume · c: clear buffer · y: yank   │
│ all visible · esc: stop and back to streams                       │
└────────────────────────────────────────────────────────────────────┘
```

While paused the goroutine keeps draining the channel (else server backpressure kicks in), but the viewport stops auto-scrolling and `liveTailLineMsg` events are queued into a parking buffer that drains when the user resumes. Buffer capped at 5k lines to keep memory bounded.

## Filter

`/` opens a textinput on top of the viewport; the input is a Go regex (`regexp.Compile`). Matching lines remain in the viewport, non-matching are hidden until the filter clears. Filter is local-only - the server keeps sending everything.

The `aws logs tail` shell-out remains available as a fallback under a separate key (`T`, shift-t) since it's still the right move for very long-running tails where the TUI's 5k-line cap would be a problem.

## Reconnect handling

`StartLiveTail` sessions max out at 1 hour server-side; the SDK returns an `io.EOF`-flavoured error. The pump goroutine detects that and emits `liveTailEndedMsg{reason: "session limit, reconnecting"}`; the view immediately re-launches the pump. Other errors (auth, throttling) surface in the status footer.

## Cost

`StartLiveTail` is billed per filtered event scanned. Same cost model as `FilterLogEvents` so users running `aws logs tail` today pay the same.

## Acceptance criteria

- `t` on a log group opens the in-TUI tail view.
- Events stream in real time; the viewport auto-scrolls.
- `p` pauses auto-scroll; `p` again resumes.
- `c` clears the visible buffer (server keeps streaming).
- `/` filters by regex; invalid regex shows an inline error and doesn't break the stream.
- `y` yanks the visible (filtered) lines to clipboard.
- `esc` cleanly closes the stream and returns to the streams list.
- Session limit hits trigger an automatic reconnect; status footer reports it briefly.
- Buffer is capped at 5k lines; oldest drop first.
- `T` (shift-t) still falls back to the existing `aws logs tail --follow` shell-out for unbounded sessions.
