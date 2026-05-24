# Contributing to aws-tui

This is primarily a personal tool. PRs and issues are welcome - there's no SLA
on response time. If you want to take it in a substantially different direction,
fork freely; the MIT licence is there for exactly that.

## Local setup

```bash
git clone https://github.com/huy-tran/aws-tui
cd aws-tui
go test ./...
make install        # POSIX
.\build.ps1 -Install   # Windows
```

`make install` (or `build.ps1 -Install`) puts the binary at `$(go env GOBIN)/aws-tui`
with the right `-ldflags` so the title bar shows your git tag + your git `user.name`.

You'll need:

- Go 1.22+
- `aws` CLI v2 (for the SSM session and `T` shellout tail; the rest of the app
  uses the SDK directly)
- `session-manager-plugin` if you want to test the SSM flows
- An authenticator app for the TOTP gate (or set `AWS_TUI_TOTP_TTL=0` after
  enrolling, then `--lock` between tests)

## Architecture

The spec files in [`specs/`](./specs/) are the canonical reference and were
written before the code. Read [`specs/02-architecture.md`](./specs/02-architecture.md)
first - it covers the view-stack model and the navigation message types.
Everything else builds on that.

Most subsequent specs are scoped to a single feature (one tab, one piece of
infrastructure). When you add a new feature, write the spec first - the
existing specs are short and focused; aim for similar.

## Code conventions

- One package per view under `internal/views/`.
- Each view exposes `New(...)` and implements `tea.Model`.
- AWS API calls always go inside a `tea.Cmd` returning a typed message. Never
  block in `Update`.
- Errors bubble as `errMsg{err}` typed messages. The view renders them; nothing
  panics on an AWS-side failure.
- Long-running shell-outs (SSM, port-forward, log tail fallback) use
  `tea.ExecProcess` so the TUI suspends cleanly.
- Use `gopkg.in/ini.v1` for parsing `~/.aws/config`, not custom parsing.
- Persisted writes are atomic (`.tmp` file + rename).
- Use Australian / British English in code comments and any UI string
  ("colour", "centre", "organisation").

## Testing

```bash
go test ./...
```

Tests are unit-only - no live AWS calls. Each view has `*_test.go` covering the
filter on/off transition, mode transitions, and the `CapturingInput` / `InSubnav`
interface contracts. The `datatable` widget has its own tests for sort, border
rendering, and the ANSI-truncate fix. Use `t.TempDir()` for anything that
touches the filesystem.

## Things that are intentionally out of scope

See the "Non-goals" section in [`README.md`](./README.md). The short version:
no IAM admin, no cost views, no cross-account aggregation, no rebuilding
features that `aws` CLI does well (we shell out where appropriate). If a PR
adds something on that list, expect a polite pushback unless it ships with a
strong workflow argument.

## Releasing

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
make install        # picks up the new tag automatically
```

Cross-platform binaries can be built into `dist/` via `make dist` or
`.\build.ps1 -Dist`. Add a `goreleaser` config later if/when binaries should
attach to GitHub releases.
