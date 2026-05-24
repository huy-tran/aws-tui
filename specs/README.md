# aws-tui specs

Design documents, one per subsystem. These were written **before** the code
as the working blueprint - every feature has a spec that captures the
intended shape, layout, acceptance criteria, and the trade-offs considered
at design time.

> **The code is authoritative.** Where a spec and the current source disagree,
> the source wins. Some specs have minor drift (e.g. `06-main-dashboard.md`
> shows letter hotkeys that the dashboard later removed). Use the specs to
> understand the design rationale and the original scope; read the code
> for current behaviour.

## Goal

Replace the friction of remembering AWS CLI commands and arguments with a
fast, keyboard-driven TUI that scopes work to one profile + region at a time.

## Core flow

```
Profile picker  ->  Region picker  ->  Main dashboard (tabbed services)
```

The user selects a profile, then a region, then lands in a tabbed dashboard
where each tab is a different AWS service. Profile and region can be
switched at any time via hotkeys.

## Stack

- **Language:** Go (1.22+)
- **TUI framework:** [Bubble Tea](https://github.com/charmbracelet/bubbletea)
- **Components:** [Bubbles](https://github.com/charmbracelet/bubbles) (list, table, textinput, spinner, viewport, textarea)
- **Styling:** [Lipgloss](https://github.com/charmbracelet/lipgloss) + lipgloss/table for the per-cell-bordered datatable widget
- **AWS SDK:** [aws-sdk-go-v2](https://github.com/aws/aws-sdk-go-v2)
- **TOTP:** [pquerna/otp](https://github.com/pquerna/otp) + [mdp/qrterminal](https://github.com/mdp/qrterminal)
- **Config parsing:** `gopkg.in/ini.v1`
- **Fuzzy filtering:** built-in via `bubbles/list`

## Spec index

The specs roughly correspond to the order the project was built in. Each
file is self-contained: an architecture sketch, an ASCII mockup of the
layout, the implementation outline, acceptance criteria.

### Foundations

1. [`01-project-setup.md`](./01-project-setup.md) - Go module, dependencies, directory layout
2. [`02-architecture.md`](./02-architecture.md) - Bubble Tea model structure, navigation message types, view-stack pattern
3. [`03-aws-client.md`](./03-aws-client.md) - AWS SDK wrapper, credential handling, SSO expiry surface
4. [`12-state-and-caching.md`](./12-state-and-caching.md) - State persistence, in-memory TTL cache layer
5. [`13-build-and-distribute.md`](./13-build-and-distribute.md) - Cross-platform builds, release process

### Entry flow

6. [`04-profile-picker.md`](./04-profile-picker.md) - First screen, profile list, fuzzy filter
7. [`05-region-picker.md`](./05-region-picker.md) - Region selection screen
8. [`06-main-dashboard.md`](./06-main-dashboard.md) - Tabbed dashboard shell, header bar, hotkeys

### Service tabs

9. [`07-ec2-view.md`](./07-ec2-view.md) - Instance list, SSM session, port-forward
10. [`08-cloudfront-view.md`](./08-cloudfront-view.md) - Distributions, invalidations
11. [`09-s3-view.md`](./09-s3-view.md) - Bucket and object browser
12. [`10-beanstalk-view.md`](./10-beanstalk-view.md) - Environments, health, events, deploys
13. [`11-cloudwatch-view.md`](./11-cloudwatch-view.md) - Log groups, streams, search
14. [`14-parameter-store-view.md`](./14-parameter-store-view.md) - SSM Parameter Store list / value / edit / history
15. [`15-securityhub-view.md`](./15-securityhub-view.md) - Insights -> results -> findings -> detail, read-only
16. [`21-rds-view.md`](./21-rds-view.md) - RDS instances, SSM port-forward command builder

### Platform features

17. [`16-help-overlay.md`](./16-help-overlay.md) - `?` overlay listing keybindings for the active screen
18. [`17-status-footer.md`](./17-status-footer.md) - Persistent bottom-of-screen bar
19. [`18-dry-run-mode.md`](./18-dry-run-mode.md) - `--dry-run` flag and `~/.aws-tui/audit.log`
20. [`19-persistent-cache.md`](./19-persistent-cache.md) - On-disk per-profile cache that survives restarts
21. [`20-sort-by-column.md`](./20-sort-by-column.md) - `s` then column digit; ascending/descending toggle
22. [`23-securestring-mask.md`](./23-securestring-mask.md) - SecureString mask by default + 30s auto-remask
23. [`24-totp-lock.md`](./24-totp-lock.md) - TOTP launch gate with 4h unlock window and backup codes
24. [`25-bookmarks.md`](./25-bookmarks.md) - `b` toggle, `B` list, per-profile persisted
25. [`26-region-availability.md`](./26-region-availability.md) - Region picker probes `DescribeRegions` for opt-in badges
26. [`27-in-tui-tail.md`](./27-in-tui-tail.md) - In-TUI live log tail via `StartLiveTail`
27. [`28-theme-tuning.md`](./28-theme-tuning.md) - Dark / light / auto colour palette

### Quality

28. [`22-unit-tests.md`](./22-unit-tests.md) - Per-view test patterns and acceptance criteria

## Non-goals

- Resource creation / deletion beyond CloudFront invalidations and Beanstalk deploys
- IAM management
- Cost / billing views
- Cross-account aggregation (one profile + one region, always)
- Reimplementing tools that already do their job well (`aws logs tail`, `session-manager-plugin`); we shell out where it makes sense

## Design principles

- **Single profile context.** Never aggregate across profiles.
- **Lazy loading.** No AWS API calls before a view opens.
- **Cache aggressively.** TTL on list operations; manual refresh with `r`.
- **Shell out, don't reimplement.** SSM sessions and the `T` tail fallback delegate to the AWS CLI.
- **Fail loudly on auth.** Detect expired SSO and surface the exact `aws sso login` command inline.
- **Persistent context header.** Always show profile + region + service, colour-coded by environment.
- **Honest about security.** The TOTP gate is friction, not cryptography. SecureString masking is shoulder-surfing defence, not anti-user. The threat model assumes the OS user account is the trust boundary.
