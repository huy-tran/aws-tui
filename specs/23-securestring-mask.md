# 23 — SecureString Mask & Auto-Re-mask

Today opening a SecureString parameter shows the decrypted value indefinitely. Leave the TUI open while you walk away from your desk and the secret is sitting on screen for anyone to read.

This spec adds:

1. **Mask by default.** SecureString values open masked; you have to press `r` to reveal.
2. **Auto-mask after idle.** Once revealed, 30 seconds of no keystroke re-masks the value automatically.
3. **Yank always works on the raw value.** Masking is a visual concern; `y` and `Y` copy the real secret either way. Masking doesn't pretend to protect against "user can interact with the TUI" - it protects against passive bystanders.

Non-SecureString parameter types (String, StringList) are unaffected.

## What does masked look like?

The structure is preserved (line count, whitespace) but every printable character is replaced with `•`:

```
Value (masked · press 'r' to reveal)
┌──────────────────────────────────────────────────────────┐
│ ••••••••••••••••••••••••••••••••                         │
│ •••••••• ••• •••••••••••••                                │
│ •••••••••••••                                             │
└──────────────────────────────────────────────────────────┘
```

When revealed:

```
Value (decrypted · re-masks after idle · press 'r' to mask)
┌──────────────────────────────────────────────────────────┐
│ s3cr3t-p@ssw0rd-redacted                                  │
│ another line of the value                                 │
│ etc                                                       │
└──────────────────────────────────────────────────────────┘
```

The label tells you the state and the next keystroke; you never have to guess.

## Reveal / mask interaction

- `r` toggles reveal ↔ mask while viewing a SecureString.
- Any keystroke in the value view bumps the idle timer (so scrolling / yank-line don't trigger an early mask).
- The mask check runs on a `tea.Tick` scheduled for the remaining time. If the user did nothing, the tick re-masks; if they did, the tick reschedules.
- Re-entering the value view (back to list, drilling in again, viewing a different version) re-masks - reveal is per-view, not per-session.
- History-loaded versions of a SecureString get the same treatment.

## Yank semantics

- `y` (yank full value) writes the raw decrypted value to the clipboard whether masked or revealed.
- `Y` (yank current line) writes the real line from the underlying value, not the masked-dot rendering of it.

The audit log already records "Parameter Store · y" / "Parameter Store · Y" actions via the status footer prefix; nothing changes there.

## Timeout

30 seconds, hardcoded for now. A future spec can promote this to a config flag (`--secure-idle=Ns` / `AWS_TUI_SECURE_IDLE`) - the field-level constant is in one place if we ever want to expose it.

## Status surface

While revealed, the value's title row shows the "re-masks after idle" hint. No countdown - the countdown number stale-rendering between keystrokes would be more distracting than useful, and the user knows "if I'm not touching the keyboard, it'll mask soon."

## Out of scope

- Per-value PIN gates (would require OS keychain to be meaningfully secure - large extra scope).
- Different timeouts per parameter (would need per-param metadata; not warranted yet).
- Masking the value while it sits in the audit log payload — already only metadata; never the raw bytes (see spec 18).

## Acceptance criteria

- Opening a SecureString opens masked; the label says so.
- `r` reveals; the label updates and a fresh 30s timer starts.
- 30 seconds of no key in the value view re-masks; the label updates.
- Pressing any key (cursor move, scroll, yank-line) while revealed bumps the timer.
- `y` and `Y` copy the real value, masked or not.
- Closing the value view (esc) resets to "masked" on next open, no leakage of reveal state.
- String / StringList values are unaffected by the mask machinery.
