# 26 — Region Availability Badges

The region picker shows a hard-coded list of common AWS regions. The user doesn't know which of those the **active profile** can actually hit (opt-in regions exist but require an explicit `aws ec2 enable-region` style activation per account). Today you pick a region, the dashboard tries to load, and only then do you discover the region wasn't opted in for this profile.

This spec adds a one-time probe at the picker: `ec2.DescribeRegions(AllRegions: true)` returns every region the profile knows about with an `OptInStatus`. A badge next to each region in the picker tells you whether it's good to go.

## Probe result

`OptInStatus` values from the SDK:

| Status                | Meaning                                                                                 | Badge        |
|-----------------------|-----------------------------------------------------------------------------------------|--------------|
| `opt-in-not-required` | Always-on region (us-east-1 etc).                                                       | green ✓      |
| `opted-in`            | Opt-in region the profile has already enabled.                                          | green ✓      |
| `not-opted-in`        | Opt-in region the profile has **not** enabled. Selecting it will fail.                  | yellow ⚠     |
| missing from response | Unknown - either the call failed or the region isn't in this account's list.           | muted —       |

The probe is best-effort. If credentials aren't ready (SSO expired etc), badges just don't show; the picker falls back to today's behaviour.

## Layout

```
> ap-southeast-2 · Sydney        ✓
  ap-southeast-1 · Singapore     ✓
  ap-southeast-4 · Melbourne     ⚠ opt-in needed
  ap-northeast-1 · Tokyo         ✓
  us-east-1      · N. Virginia   ✓
  me-south-1     · Bahrain       ⚠ opt-in needed
```

The description column of each `list.Item` carries the badge so it sits inline with the region name without rejiggering the bubbles/list layout.

## Implementation

In `internal/views/region/region.go`:

1. `regionItem` gains an optional `badge string` field. `Description()` returns `name + "  " + badge` (with a styled badge when non-empty).
2. On `Init()`, the model returns a `probeRegionsCmd` that calls `ec2.DescribeRegions` with `AllRegions: true`. The cmd builds `map[string]string` of region -> badge.
3. The model handles `probeResultMsg`: it rebuilds the `list.Item`s with badges populated and calls `list.SetItems`.
4. On probe failure (`probeErrMsg`), nothing happens - badges just stay empty.

## Caching

The aws.Cache already exists; this probe caches under `ec2:opt-in-regions:` for 30 minutes. Reopening the picker within the window skips the live call.

## Acceptance criteria

- Opening the region picker kicks off a single `DescribeRegions` call (or hits cache).
- Each common region's description gets a `✓` / `⚠` badge once results arrive.
- Selecting a `⚠` region still proceeds (the user might be about to opt in another way).
- Probe failure or missing credentials degrades silently to no badges.
- Repeated picker opens within 30 minutes use the cache, no extra API call.
