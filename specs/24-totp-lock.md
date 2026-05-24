# 24 — TOTP Launch Lock

Gate the app behind a TOTP (Authenticator-app) code at launch. Once entered, the unlock persists for **4 hours** before the next prompt. This is friction against casual / passive misuse - the realistic "someone walked up to my unattended terminal" threat. It is **not** marketed as security: anyone with shell access to `~/.aws-tui/` can read the TOTP secret or `rm` the marker, and they could also just run `aws` directly. The point is to make destructive aws-tui actions not a one-keystroke walk-up.

## Files

- `~/.aws-tui/totp.secret` (0600) - the base32 TOTP secret, generated on first run.
- `~/.aws-tui/unlock.marker` (0600) - one line: `RFC3339` timestamp the trust expires at. Absent / expired -> re-prompt.
- `~/.aws-tui/backup.codes` (0600) - JSON `["sha256:..."]` array; 10 codes generated at first-run, each consumed-on-use for the "phone died" path.

All three live under the existing `~/.aws-tui/` directory (same place audit log / cache / state already live), 0700 directory.

## First-run flow

Triggered when `totp.secret` is missing.

1. Print a setup screen to stdout (before Bubble Tea starts):
   ```
   aws-tui: first-time setup

   Scan this QR with Google Authenticator / 1Password / Authy:

     [QR code rendered with mdp/qrterminal in the terminal]

   Or enter this secret manually:
     <base32 secret>

   Account: aws-tui:<system username>
   Issuer:  aws-tui

   Enter the current 6-digit code to confirm: _
   ```
2. Verify the typed code against the freshly-generated secret. On mismatch, the secret is discarded and the user can retry.
3. On success, print 10 backup codes:
   ```
   Save these one-time backup codes somewhere safe (you won't see
   them again). Each works once if you lose access to your code app:

     ABCDE-FGHJK
     ZXYWV-UTSRQ
     ...

   Press enter to continue.
   ```
4. Persist `totp.secret` (plaintext base32; the dir's 0700 is the trust boundary) and `backup.codes` (sha256 of each code).
5. Continue into the normal unlock-marker flow (the just-verified code counts as the first unlock).

## Launch flow

1. Skip auth entirely for `--version` / `--help` / `--lock` / `--reset-totp` since they don't touch AWS.
2. Read `unlock.marker`. If present and `time.Now() < parsed_expiry`, continue to Bubble Tea unchanged.
3. Otherwise, prompt:
   ```
   aws-tui is locked. Enter TOTP code (or backup code): _
   ```
4. 6 digits -> validate via TOTP. 11 chars with a `-` -> validate against backup codes list (remove on match).
5. After 3 wrong attempts in a row, back off 2s, doubling each failure, capped at 30s. Don't lock out permanently - that's denial-of-service against the legitimate user.
6. On success, write `unlock.marker` with expiry `time.Now().Add(4h)`. Launch Bubble Tea.

## CLI

- `--lock` - wipe `unlock.marker` and exit `0`. Idempotent. Skips auth (you can lock without unlocking).
- `--reset-totp` - confirm "are you sure" then wipe `totp.secret`, `backup.codes`, `unlock.marker`. Next launch runs first-run setup. Skips auth (recovery path).
- `--totp-ttl=Nh` / `AWS_TUI_TOTP_TTL=Nh` (e.g. `8h`, `0` to always prompt) - override the 4h default for this launch.

## Implementation

New package `internal/auth` with:

```go
package auth

type Config struct {
    Issuer      string
    AccountName string
    TTL         time.Duration
}

// Authenticate runs the launch gate. It prints prompts to stdout and
// reads from stdin. Returns nil on success; non-nil on user cancel
// (^D / ^C during a prompt).
func Authenticate(cfg Config) error

// Lock wipes the unlock marker so the next launch prompts again.
// Safe to call when no marker exists.
func Lock() error

// ResetTOTP wipes the TOTP secret + backup codes + unlock marker.
// Next launch starts first-run setup.
func ResetTOTP() error
```

Dependencies:
- `github.com/pquerna/otp/totp` for TOTP generate/validate.
- `github.com/mdp/qrterminal/v3` for terminal QR rendering.

`cmd/aws-tui/main.go` calls `auth.Authenticate(...)` once before `tea.NewProgram`. CLI flag parsing already lives in main; add `--lock` / `--reset-totp` / `--totp-ttl` next to `--dry-run`.

## Why not stronger

- **OS keychain.** Cross-platform integration story is ugly for a console app on Windows; the marginal security gain is small when the threat model is already "I trust the OS user."
- **YubiKey.** Genuine security upgrade, but requires hardware. We can layer this later if the user invests.
- **No fallback / no recovery codes.** Realistic phone-loss scenario; need a path back in. `--reset-totp` plus backup codes give that.

## Acceptance criteria

- First launch with no secret runs through setup (QR + manual + backup codes) and writes the three files.
- Subsequent launches inside the 4h window proceed silently.
- Launches outside the window prompt for code; 6 digits validates against TOTP, 11-char dash-separated validates against backup codes (each one-shot).
- Wrong code triggers exponential backoff capped at 30s.
- `--lock` removes the marker.
- `--reset-totp` confirms then wipes the three files.
- `--totp-ttl` / `AWS_TUI_TOTP_TTL` override the 4h default.
- `--version` / `--help` / `--lock` / `--reset-totp` work without unlocking.
- All files are 0600; the parent dir is 0700.
- Backup codes are stored as sha256 hashes, not plaintext.
- Tests cover: marker fresh / expired / missing parsing, backup-code one-shot consumption, the TTL parser, exponential-backoff schedule. No tests touch real `~/.aws-tui/`; everything uses `t.TempDir()`.
