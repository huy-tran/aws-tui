# 16 — Help Overlay

`?` opens a modal-style overlay listing every keybinding for the currently active view (and the global ones that always apply). The footer already promises `? help`; this delivers it.

## Layout

```
┌─ Help · EC2 ───────────────────────────────────────────────────┐
│                                                                 │
│ Global                                                          │
│   ctrl+c       quit                                             │
│   ctrl+p       switch profile                                   │
│   ctrl+r       switch region                                    │
│   tab / S-tab  next / previous tab                              │
│   ←/→          next / previous tab (root list, no input)        │
│   ?            toggle this help                                 │
│                                                                 │
│ EC2 list                                                        │
│   enter        SSM start-session into selected instance         │
│   p            port-forward modal                               │
│   i            instance details                                 │
│   /            filter by name / id / ip / tag                   │
│   r            refresh                                          │
│   y i / y p    yank instance id / private IP                    │
│   ↑/↓ j/k      move cursor                                      │
│   pgup/pgdn    page                                             │
│                                                                 │
│   esc          close help                                       │
└─────────────────────────────────────────────────────────────────┘
```

Content is bound to the active tab: open it on the CloudFront tab and the second section becomes "CloudFront list". When inside a sub-mode (Beanstalk details, Paramstore edit form, etc.) the section reflects that mode instead of the root list.

## Source of truth

Each view that wants help in the overlay implements `HelpItems() []help.Section`. The help dispatcher in `internal/ui/help` already knows the global section; it concatenates the view's sections on top.

```go
package help

type Item struct {
    Keys string  // "enter" / "↑/↓" / "y i / y p"
    Desc string
}

type Section struct {
    Title string
    Items []Item
}
```

Views that don't implement `HelpItems()` still get the global section (so the overlay is never empty).

## Toggle semantics

- `?` opens the overlay from anywhere except a text input (filter, edit, confirm). Inside an input it's literal punctuation and goes to the field.
- `esc` closes it. `?` again also closes it (toggle).
- While the overlay is open, all other key handling is suppressed — the underlying view does not advance state. This is intentional: the overlay is a read-only reference, not a command palette.

## App layer

The overlay lives at the **app** level, not inside dashboard, so it works equally well over pushed sub-views (EC2 details, S3 browser, port-forward modal). `internal/app/app.go` owns a single `helpOpen bool` and gates Update / View:

```go
case tea.KeyMsg:
    if msg.String() == keyToggleHelp && !m.topCapturingInput() {
        m.helpOpen = !m.helpOpen
        return m, nil
    }
    if m.helpOpen {
        if msg.String() == "esc" { m.helpOpen = false; return m, nil }
        return m, nil   // swallow everything else
    }
    // ... rest of key handling
```

`topCapturingInput()` reuses the existing `inputCaptor` interface (so `?` in a focused filter / textarea is typed instead of toggling the overlay).

## Rendering

The overlay renders as a centered box over the underlying view. lipgloss' `Place` handles centering:

```go
func (m Model) View() string {
    title := m.renderTitle()
    body  := m.stack[len(m.stack)-1].View()
    base  := lipgloss.JoinVertical(lipgloss.Left, title, body)
    if !m.helpOpen { return base }
    overlay := help.Render(m.activeViewName(), m.activeViewHelpItems(), m.width, m.height)
    return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, overlay,
        lipgloss.WithWhitespaceChars(" "))
}
```

`activeViewName()` returns "EC2", "Parameter Store · edit", etc. — useful so the user knows which mode's keys are listed.

## Acceptance criteria

- `?` from any root list opens the overlay; the overlay shows global keys + view-specific keys.
- `?` from inside a text input (filter, edit form, prod-confirm) types the character.
- `esc` and `?` both close the overlay.
- While open, other keys are no-ops (don't tick spinners, don't switch tabs).
- Overlay is centered, with a clear title naming the active view.
- Every existing view either implements `HelpItems()` (preferred) or gracefully shows just the global section.
