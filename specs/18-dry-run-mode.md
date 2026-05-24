# 18 — Dry-Run Mode

A global flag (`--dry-run` or env `AWS_TUI_DRY_RUN=1`) that intercepts every destructive AWS call. Instead of executing, the call is recorded and the view treats it as if the call succeeded. The user sees what they would have done, with the safety net that nothing actually happened.

This makes adding new write features much less scary: a brand-new "delete bucket" key can be tested end-to-end without risking a real delete. It also gives auditable history of intent: every blocked call lands in an audit log on disk regardless of whether dry-run is on.

## What counts as destructive

Today's write-paths in the codebase:

- CloudFront `CreateInvalidation` (and the `/*` wildcard variant).
- Beanstalk `UpdateEnvironment` (deploy).
- Parameter Store `PutParameter` (edit / create).

When dry-run is on, those calls are not issued. Instead a recorder is told what would have been sent and returns a successful response shape. The view sees the same success path as a real call.

## Reads stay live

Read calls (`DescribeInstances`, `GetParameter`, `GetFindings`, etc.) always go out for real. Dry-run is about preventing mutations, not pretending to load.

## Audit log

Both real and dry-run writes append a JSON line to `~/.aws-tui/audit.log`:

```json
{"ts":"2026-05-24T10:14:32Z","profile":"prod-x","region":"ap-southeast-2","action":"cloudfront:CreateInvalidation","target":"E1ABCDEF","payload":{"paths":["/*"]},"dry_run":false,"result":"I2XYZ987"}
{"ts":"2026-05-24T10:18:08Z","profile":"prod-x","region":"ap-southeast-2","action":"ssm:PutParameter","target":"/app-prod/db/password","payload":{"type":"SecureString","value_bytes":24},"dry_run":true}
```

Secret-bearing payloads (Parameter Store values, anything tagged sensitive) record only metadata — the byte count, the type, the version number — never the value itself. This way the audit log is safe to commit / share / forward to the security team.

## Status surface

When dry-run is on, the title bar gets a yellow `[DRY-RUN]` suffix so the user can't forget:

```
              AWS TUI - v0.0.4  [DRY-RUN]
```

The status footer also reflects the most recent dry-run action ("dry-run: invalidate E1ABCDEF /*" instead of "created invalidation I2XYZ987").

## Implementation

New package `internal/audit`:

```go
package audit

type Action struct {
    Profile string
    Region  string
    Action  string             // e.g. "cloudfront:CreateInvalidation"
    Target  string             // resource id
    Payload map[string]any     // safe-to-log payload metadata
}

// Log appends one record. Returns nil even if writing fails - audit is
// best-effort and must never block a real workflow.
func Log(a Action, dryRun bool, result string) error

// Mode is consulted by the views to decide whether to short-circuit a write.
type Mode struct {
    DryRun bool
}

var current Mode
func SetMode(m Mode) { current = m }
func IsDryRun() bool { return current.DryRun }
```

Views check `audit.IsDryRun()` before issuing a write. If true, they call `audit.Log` with the intended action and skip the SDK call, jumping straight to the success message path. The success message includes the `dry-run:` prefix so it's obvious in the status footer / view body.

## CLI plumbing

`cmd/aws-tui/main.go` parses `--dry-run` (and `AWS_TUI_DRY_RUN`) and calls `audit.SetMode(audit.Mode{DryRun: true})` before starting the Bubble Tea program.

## Acceptance criteria

- `--dry-run` flag (and env) enables the mode end-to-end.
- Every CreateInvalidation / UpdateEnvironment / PutParameter call is intercepted; the SDK is not called.
- The view body / status footer makes it clear the action was a dry-run (prefix "dry-run: ...").
- Title bar shows `[DRY-RUN]` suffix when on.
- Audit log records every write attempt (real or dry) at `~/.aws-tui/audit.log` as one JSON object per line; values for Parameter Store payloads are redacted to metadata only.
- The audit logger never panics or fails the surrounding workflow; write errors are swallowed (best-effort).
