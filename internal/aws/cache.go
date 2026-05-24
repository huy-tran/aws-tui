package aws

import (
	"bytes"
	"encoding/gob"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// cacheFileVersion is the leading byte of the on-disk format. Bump if the
// envelope ever needs to change wholesale; field-level changes inside
// individual cached structs are handled by gob's own forgiving decoder.
const cacheFileVersion byte = 1

// debounceWindow coalesces a burst of Set/Invalidate calls into a single
// disk write.
const debounceWindow = 300 * time.Millisecond

type cacheEntry struct {
	Value     interface{}
	ExpiresAt time.Time
}

type Cache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry

	// Disk-backed config. path == "" means memory-only (used by tests).
	path        string
	saveTimer   *time.Timer
	saveRunning bool
}

// NewCache returns a memory-only cache - kept for tests and any caller
// that doesn't want persistence.
func NewCache() *Cache {
	return &Cache{entries: map[string]cacheEntry{}}
}

// NewPersistentCache returns a cache backed by the given file path. The
// file is loaded on construction (missing / corrupt = empty); subsequent
// Set / Invalidate calls schedule a debounced atomic rewrite.
func NewPersistentCache(path string) *Cache {
	c := &Cache{entries: map[string]cacheEntry{}, path: path}
	c.loadFromDisk()
	return c
}

func (c *Cache) Get(key string) (interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.ExpiresAt) {
		delete(c.entries, key)
		c.scheduleSaveLocked()
		return nil, false
	}
	return entry.Value, true
}

func (c *Cache) Set(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{
		Value:     value,
		ExpiresAt: time.Now().Add(ttl),
	}
	c.scheduleSaveLocked()
}

func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
	c.scheduleSaveLocked()
}

func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	c.entries = map[string]cacheEntry{}
	path := c.path
	c.mu.Unlock()
	if path != "" {
		_ = os.Remove(path)
	}
}

// scheduleSaveLocked starts (or extends) the debounce timer. Must be
// called with c.mu held.
func (c *Cache) scheduleSaveLocked() {
	if c.path == "" {
		return
	}
	if c.saveTimer != nil {
		c.saveTimer.Stop()
	}
	c.saveTimer = time.AfterFunc(debounceWindow, c.flushToDisk)
}

// flushToDisk snapshots the live entries and writes them atomically. Errors
// are intentionally swallowed - persistence is best-effort.
func (c *Cache) flushToDisk() {
	c.mu.Lock()
	if c.path == "" {
		c.mu.Unlock()
		return
	}
	// Snapshot so we can release the lock before doing I/O.
	snapshot := make(map[string]cacheEntry, len(c.entries))
	now := time.Now()
	for k, v := range c.entries {
		if now.After(v.ExpiresAt) {
			continue
		}
		snapshot[k] = v
	}
	path := c.path
	c.mu.Unlock()

	if err := writeCacheFile(path, snapshot); err != nil {
		return
	}
}

func writeCacheFile(path string, entries map[string]cacheEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := buf.WriteByte(cacheFileVersion); err != nil {
		return err
	}
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(entries); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadFromDisk populates the in-memory map from c.path. Damaged file,
// unknown types, or version mismatch all degrade silently to "empty
// cache" - never returned as an error.
func (c *Cache) loadFromDisk() {
	if c.path == "" {
		return
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	if len(data) < 1 || data[0] != cacheFileVersion {
		return
	}
	dec := gob.NewDecoder(bytes.NewReader(data[1:]))
	var entries map[string]cacheEntry
	if err := dec.Decode(&entries); err != nil {
		// Schema mismatch (registered type removed, struct rename, etc).
		// Drop the file rather than carry around broken state.
		_ = os.Remove(c.path)
		return
	}
	now := time.Now()
	c.mu.Lock()
	for k, v := range entries {
		if now.After(v.ExpiresAt) {
			continue
		}
		c.entries[k] = v
	}
	c.mu.Unlock()
}

// CachePath returns the per-profile cache file path under the user's home
// directory. Returns empty string if the home dir cannot be resolved.
func CachePath(profile string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	if profile == "" {
		return ""
	}
	return filepath.Join(home, ".aws-tui", "cache", profile+".gob")
}

// ensure errors package is referenced even if all paths return swallowed
// errors; helps go vet on platforms where the unused import lint differs.
var _ = errors.New
