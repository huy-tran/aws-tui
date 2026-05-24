# 19 — Persistent Cache

The in-memory `aws.Cache` dies with the process. After a restart every view re-issues its paginated `DescribeX` even though the data was usually still fresh (60s TTL on most, 5m on buckets, 10m on insights). Persisting the cache to disk between runs makes a relaunched aws-tui instantly usable instead of waiting ~5s for every tab to load.

## What persists, what doesn't

- **Read responses:** list metadata (instances, buckets, distributions, log groups, parameters, insights, etc). Persisted.
- **Decrypted parameter values:** never. Already a documented invariant in `specs/14-parameter-store-view.md`; this spec doesn't change it.
- **Hub findings:** persisted at the configured 60s TTL like everything else.
- **AWS credentials / SSO tokens:** untouched. The SDK already manages those itself.

## Disk layout

One file per profile so different accounts don't share a cache file:

```
~/.aws-tui/
  cache/
    prod-x.gob
    staging-x.gob
    dev-x.gob
  state.json   (existing - profile / region prefs, bucket regions, etc)
  audit.log    (from spec 18)
```

The file is a single `gob`-encoded `map[string]cacheEntry` where each entry includes its `expiresAt`. Stale entries are skipped on load.

`gob` is the encoder of choice because the cache holds heterogeneous `interface{}` values (one entry is `[]Instance`, the next is `[]Bucket`, etc.). Each cached type registers itself in its package's `init()`:

```go
func init() {
    gob.Register([]Instance(nil))
}
```

JSON would need a discriminator + wrapper around every value; gob's type-name machinery handles that for us.

## Lifecycle

- `aws.NewContext(profile)` resolves the cache path from `~/.aws-tui/cache/<profile>.gob` and loads any existing entries. Missing file = empty cache, no error.
- Every `Cache.Set` and `Cache.Invalidate` triggers a debounced disk write (300ms after the last mutation, single pending write coalesces multiple set/invalidate calls).
- `Cache.InvalidateAll` deletes the file too.
- Writes are atomic: write to `<path>.tmp`, fsync, rename — same pattern as `internal/state` uses for `state.json`.

## Safety on schema change

`gob` is permissive but not magic. If a cached struct gains or loses a field between releases, decoding skips the missing/extra field — usually fine. If a struct's package path or name changes, decoding fails for that entry; we treat decode failures per-entry as cache misses (drop the entry, keep loading the rest). No way to corrupt the surrounding workflow.

A single top-level version byte at the head of the file lets us bump the format wholesale if needed; today it's `1`.

## TTL handling

The `expiresAt` is absolute time. After a restart the cache contains entries with future, present, or past expiration. `Get` already drops anything past `expiresAt`, so the disk reload doesn't need to filter — but we *do* purge expired entries on load to keep the file small.

## Acceptance criteria

- Restarting aws-tui surfaces lists immediately when the on-disk cache is fresh, with no API call.
- Restarting after the TTL elapsed triggers a refresh as today.
- Decrypted Parameter Store values never touch the cache file (already true; explicit invariant kept).
- Different profiles use separate cache files; switching profile within the same process uses the right file.
- Damaged or stale-schema cache entries are silently dropped (logged at most) without crashing.
- `~/.aws-tui/cache/<profile>.gob` is written with 0600 permissions to match the rest of the app's on-disk state.
- The cache file is excluded from git via `.gitignore` (paranoia — it's in `$HOME`, not the repo, but the user might one day relocate).
