# 12 — State and Caching

Two separate concerns:

1. **Persisted state** (`internal/state`) — written to disk, survives restarts. Profile, region preferences, bucket region cache.
2. **In-memory cache** (`internal/aws/cache.go`) — TTL'd cache of API responses for the current session.

## Persisted state (internal/state/state.go)

Stored as JSON at `~/.config/aws-tui/state.json` (or `%APPDATA%\aws-tui\state.json` on Windows).

```go
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type Store struct {
	mu sync.Mutex

	LastProfile    string            `json:"last_profile"`
	LastRegions    map[string]string `json:"last_regions"`     // profile -> region
	BucketRegions  map[string]string `json:"bucket_regions"`   // bucketName -> region
	UpdatedAt      string            `json:"updated_at"`
}

func configPath() (string, error) {
	var base string
	if runtime.GOOS == "windows" {
		base = os.Getenv("APPDATA")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, "AppData", "Roaming")
		}
	} else {
		base = os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".config")
		}
	}
	dir := filepath.Join(base, "aws-tui")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

func Load() (*Store, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	s := &Store{
		LastRegions:   map[string]string{},
		BucketRegions: map[string]string{},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, s); err != nil {
		// Corrupt file: log and start fresh rather than blocking startup
		return &Store{
			LastRegions:   map[string]string{},
			BucketRegions: map[string]string{},
		}, nil
	}
	if s.LastRegions == nil {
		s.LastRegions = map[string]string{}
	}
	if s.BucketRegions == nil {
		s.BucketRegions = map[string]string{}
	}
	return s, nil
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := configPath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	// Write atomically: tmp file + rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) LastRegionFor(profile string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastRegions[profile]
}

func (s *Store) SetLastRegionFor(profile, region string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastRegions[profile] = region
}

func (s *Store) GetBucketRegion(bucket string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.BucketRegions[bucket]
}

func (s *Store) SetBucketRegion(bucket, region string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BucketRegions[bucket] = region
}
```

## In-memory cache (internal/aws/cache.go)

Generic TTL cache keyed by string. Each service view caches its own list call's result.

```go
package aws

import (
	"sync"
	"time"
)

type cacheEntry struct {
	value     interface{}
	expiresAt time.Time
}

type Cache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

func NewCache() *Cache {
	return &Cache{entries: map[string]cacheEntry{}}
}

func (c *Cache) Get(key string) (interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.value, true
}

func (c *Cache) Set(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}
}

func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]cacheEntry{}
}
```

Attach a `Cache` instance to `aws.Context`:

```go
type Context struct {
	Profile string
	Region  string
	Cache   *Cache
	// ...
}

func NewContext(profile string) *Context {
	return &Context{
		Profile: profile,
		Cache:   NewCache(),
	}
}
```

## Cache key conventions

| View         | Key                                              | TTL  |
|--------------|--------------------------------------------------|------|
| EC2          | `ec2:instances:{region}`                         | 60s  |
| CloudFront   | `cloudfront:distributions`                       | 60s  |
| CloudFront   | `cloudfront:invalidations:{distID}`              | 30s  |
| S3           | `s3:buckets`                                     | 5m   |
| S3           | `s3:objects:{bucket}:{prefix}`                   | 30s  |
| Beanstalk    | `eb:environments`                                | 60s  |
| Beanstalk    | `eb:versions:{appName}`                          | 5m   |
| Logs         | `logs:groups:{region}`                           | 60s  |
| Logs         | `logs:streams:{group}`                           | 30s  |

The region is implicit when using the context's region (since the context is profile + region scoped). Include it explicitly only when the resource is global (CloudFront) or the view crosses regions (S3 buckets).

## Refresh key

`r` in any view should:

1. Invalidate the relevant cache entry.
2. Re-issue the load command.
3. Show the spinner.

```go
case "r":
	m.ctx.Cache.Invalidate("ec2:instances:" + m.ctx.Region)
	m.loading = true
	return m, m.loadCmd()
```

## What we don't cache

- Mutation responses (e.g. CreateInvalidation result is the source of truth).
- Tail/streaming data.
- Polling responses (CloudFront invalidation status while InProgress).

## Profile/region switch invalidates

When the user switches profile or region via `ctrl+p` / `ctrl+r`:

- The old `aws.Context` is discarded (or its cache invalidated).
- A new context is constructed with a fresh cache.
- Persisted state is updated.

Don't try to share cache across contexts. The complexity isn't worth it for the hit-rate gain.

## Acceptance criteria

- State file is created at first run at the platform-appropriate path.
- Last profile and per-profile last region survive restarts.
- Bucket regions are cached and avoid `GetBucketLocation` calls on subsequent launches.
- Cache TTLs match the table above.
- `r` invalidates and reloads in every list view.
- Corrupt state file falls back to defaults rather than crashing.
