# aws-tui

Terminal UI for browsing AWS resources across multiple profiles. Built in Go with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

```
TOTP unlock -> Profile picker -> Region picker -> Tabbed dashboard
              (Beanstalk · EC2 · RDS · Logs · CloudFront · S3 · Parameter Store · SecurityHub)
```

One profile, one region at a time. Switch instantly with `ctrl+p` / `ctrl+r`. Header colour-codes the current environment so prod is always red and dev is always green.

## Highlights

- **Eight services**: Elastic Beanstalk, EC2 (with SSM session + port forward), RDS (port-forward command builder), CloudWatch Logs (in-TUI live tail), CloudFront (invalidations), S3 (browser + object download), Parameter Store (read / edit / history / SecureString mask), SecurityHub (insights + findings).
- **TOTP launch gate** with a 4-hour unlock window. Backup codes for the phone-died case. `ctrl+l` to lock-and-quit mid-session.
- **Dry-run mode** intercepts every destructive call and writes intent to `~/.aws-tui/audit.log` without hitting AWS.
- **Persistent cache** survives restarts; the app opens onto already-loaded data instead of a 5-second wait.
- **Bookmarks** (`b` to toggle, `B` to list) for the same handful of resources you keep coming back to.
- **Sort by column** (`s` then the column number) on any table, including the live-tail buffer.
- **Help overlay** (`?`) lists every key for the active screen.
- **SecureString protection** - parameters open masked; `r` reveals; auto-remasks after 30s idle.

## Install

```bash
go install github.com/huy-tran/aws-tui/cmd/aws-tui@latest
```

The binary lands at `$(go env GOBIN)/aws-tui` (or `$(go env GOPATH)/bin/aws-tui`). Make sure that directory is on `PATH`.

### From source

```bash
git clone https://github.com/huy-tran/aws-tui
cd aws-tui
make install        # POSIX
.\build.ps1 -Install   # Windows
```

`install` runs `go install` with the right `-ldflags` so the title bar reflects the current git tag.

## Prerequisites

| Tool                       | Required for                                                                 |
|----------------------------|------------------------------------------------------------------------------|
| `aws` CLI v2               | SSM sessions, the `T` (shellout) tail fallback                               |
| `session-manager-plugin`   | SSM sessions (called by `aws ssm start-session`)                             |
| An authenticator app       | First-launch TOTP enrollment (Google Authenticator, 1Password, Authy, etc.)  |

The tool prints a non-fatal warning at startup if `aws` or `session-manager-plugin` is missing.

## First run

```
aws-tui
```

1. **TOTP setup** - QR code + base32 secret printed to stdout. Scan with your authenticator. Enter the 6-digit code to confirm. Save the 10 backup codes (printed once).
2. **Profile picker** - lists everything in `~/.aws/config` and `~/.aws/credentials`. `enter` to pick.
3. **Region picker** - common regions with a `✓` / `⚠ opt-in needed` badge per region (from `DescribeRegions`). `+` lets you type a custom region.
4. You land on the dashboard. Last profile + per-profile last region are remembered.

Subsequent launches within the 4-hour TOTP window skip the prompt.

## Keybindings

`?` opens an in-app overlay listing every binding for the active screen. The list below is a quick reference.

### Global

| Key                  | Action                                                              |
|----------------------|---------------------------------------------------------------------|
| `ctrl+c`             | Quit                                                                |
| `ctrl+l`             | Lock now (wipe TOTP unlock) and quit                                |
| `ctrl+p`             | Jump to profile picker                                              |
| `ctrl+r`             | Jump to region picker                                               |
| `tab` / `shift+tab`  | Next / previous dashboard tab                                       |
| `←` / `→`            | Same, from a root list (passes through in sub-screens / inputs)     |
| `b` / `B`            | Bookmark current row / open bookmarks list                          |
| `s` then `1..N`      | Sort active table by column N (repeat to flip direction)            |
| `?`                  | Toggle help overlay                                                 |
| `esc`                | View-local: clear filter, back out of a sub-mode, or pop the stack  |

### EC2

`enter` SSM start-session · `p` port-forward modal · `i` details · `/` filter · `r` refresh · `y` yank menu

### RDS

`enter` details · `f` build port-forward command (yanked, never run) · `y` yank menu (host / port / master user) · `/` filter · `r` refresh

### CloudWatch Logs

`enter` streams · `t` **in-TUI live tail** · `T` shell-out to `aws logs tail --follow` · `s` search via `FilterLogEvents` · `m` load next 50 groups · `/` filter · `r` refresh

In the in-TUI tail: `/` regex filter · `p` pause/resume · `c` clear buffer · `y` yank visible · `esc` stop

### CloudFront

`i` create invalidation · `v` view invalidations history · `/` filter · `r` refresh

Wildcard `/*` invalidations require typing `INVALIDATE` to confirm.

### S3

`enter` open bucket / download object · `y` yank menu (s3:// / https / key / bucket) · `/` filter · `r` refresh

Inside a bucket, `esc` walks up the prefix tree.

### Beanstalk

`enter` env details · `e` events · `d` deploy modal · `/` filter · `r` refresh

Prod environments (name contains `prod`) require typing the env name to confirm a deploy.

### Parameter Store

`enter` view value · `e` edit · `n` new · `h` history · `/` filter · `r` refresh

In the value view: `r` reveal/mask SecureString (auto-masks after 30s idle) · `y` yank full value · `Y` yank cursor line · `↑/↓` move cursor.

Prod paths (containing `/prod`) require typing the parameter name to confirm a save.

### SecurityHub

`enter` open insight · `a` skip insights → all active findings · `/` filter · `r` refresh

In the findings list: `1..5` toggle severity (CRIT/HIGH/MED/LOW/INFO) · `s` toggle suppressed visibility · `y` yank menu (arn / title / remediation URL).

## CLI flags

```
aws-tui --help
```

| Flag                       | Effect                                                                                                |
|----------------------------|-------------------------------------------------------------------------------------------------------|
| `--dry-run`                | Intercept every destructive call; log intent to `~/.aws-tui/audit.log`. Also `AWS_TUI_DRY_RUN=1`.     |
| `--lock`                   | Wipe the unlock marker. Next launch re-prompts.                                                       |
| `--reset-totp`             | Wipe TOTP secret + backup codes + marker. Next launch re-enrolls. Confirms via stdin.                 |
| `--totp-ttl=Nh`            | Override the 4h unlock window (0 = always prompt). Also `AWS_TUI_TOTP_TTL=Nh`.                        |
| `--theme=dark\|light\|auto` | Theme. Also `AWS_TUI_THEME`. Default `auto` (lipgloss terminal-bg probe).                             |
| `--version` / `-v`         | Print version.                                                                                        |
| `--help` / `-h`            | This list.                                                                                            |

| Env                  | Effect                                                                  |
|----------------------|-------------------------------------------------------------------------|
| `AWS_TUI_DRY_RUN=1`  | Same as `--dry-run`.                                                    |
| `AWS_TUI_TOTP_TTL`   | Same as `--totp-ttl`.                                                   |
| `AWS_TUI_THEME`      | Same as `--theme`.                                                      |

## State, cache, audit

All under `~/.aws-tui/` (`%USERPROFILE%\.aws-tui\` on Windows, mode 0700):

| File                         | Contents                                                              |
|------------------------------|-----------------------------------------------------------------------|
| `totp.secret`                | Base32 TOTP secret (0600)                                             |
| `backup.codes`               | sha256 hashes of one-shot backup codes (0600)                         |
| `unlock.marker`              | RFC3339 expiry timestamp; absent / expired -> re-prompt               |
| `audit.log`                  | One JSON line per destructive call; never contains raw secret values  |
| `cache/<profile>.gob`        | Per-profile read-cache; survives restarts; never contains decrypted parameter values |

Persistent cache TTLs match the in-memory ones (60s on most lists, 5m on S3 buckets, 10m on SecurityHub insights). `r` invalidates and refreshes.

State (last profile / region / bookmarks / known bucket regions):

| Path                                        | Platform     |
|---------------------------------------------|--------------|
| `%APPDATA%\aws-tui\state.json`              | Windows      |
| `~/.config/aws-tui/state.json`              | macOS / Linux |

## Security model — honest version

The TOTP gate is **friction, not security**. Anyone with shell access to `~/.aws-tui/` can read `totp.secret` or `rm` the marker, and they could also just run `aws` directly. The gate exists so destructive aws-tui actions aren't a one-keystroke walk-up.

- **SecureString protection** is shoulder-surfing defense: mask-by-default + 30s auto-remask. `y` still copies the raw value either way - the mask is visual.
- **Decrypted parameter values are never cached** to disk. List metadata is; the values themselves are fetched fresh on every view.
- **Dry-run mode** + the audit log give you a full record of intent vs reality for the destructive surfaces.
- **Prod-name confirms** on Beanstalk deploys, CloudFront `/*` invalidations, and Parameter Store paths containing `/prod`. Bypassable with one extra keystroke, but loud enough to catch fat-fingers.

Out of scope: OS keychain integration, FIDO2 / YubiKey, IAM session brokering, anything that pretends to defend against a determined attacker with shell access.

## Terminal compatibility

Works in: **Windows Terminal**, **PowerShell 7+**, **WSL**, and any modern Unix terminal emulator.

Avoid legacy `cmd.exe` and Git Bash (mintty) - alt-screen handling is patchy.

## Troubleshooting

- **`SSO session expired`** - the error message tells you the exact command. Run `aws sso login --profile <name>` in another shell, then press `r`.
- **SSM session won't start** - confirm `aws ssm start-session --target i-xxxx` works directly from the same shell. If it errors with "session manager plugin not found", install the plugin.
- **No profiles shown** - `aws-tui` reads `~/.aws/config` and `~/.aws/credentials`. Run `aws configure` or `aws configure sso` to create one, then restart.
- **SecurityHub empty** - SH is **regional**. Either it isn't enabled in your current region, or you're not in the aggregation region. The empty-state message has the exact `aws securityhub enable-security-hub` command for your context.
- **"Process cannot access the file" on Windows reinstall** - the OS holds `aws-tui.exe` open while it's running. Quit all instances (incl. background terminals) before `make install` / `.\build.ps1 -Install`.
- **TOTP code rejected** - confirm your phone's clock is in sync (TOTP is time-based). After 3 wrong codes the backoff starts at 2s and doubles to a 30s cap.

## Design docs

Every subsystem has a markdown spec under [`specs/`](./specs/) that captures
the intended layout, acceptance criteria, and trade-offs considered. The
code is authoritative where the two disagree; the specs are the design
rationale and the original scope.

Start with [`specs/02-architecture.md`](./specs/02-architecture.md) for the
view-stack model and navigation message types, then browse by topic from
[`specs/README.md`](./specs/README.md).

## Contributing

This is primarily a personal tool but PRs are welcome. See [CONTRIBUTING.md](./CONTRIBUTING.md) for the setup + local-development loop. No SLA on response time.

## Non-goals

- Resource creation / deletion beyond CloudFront invalidations and Beanstalk deploys
- IAM management
- Cost / billing views
- Cross-account aggregation (always one profile + one region)
- Reimplementing things `aws logs tail` and `session-manager-plugin` already do well - we shell out when it makes sense

## Licence

[MIT](./LICENSE).
