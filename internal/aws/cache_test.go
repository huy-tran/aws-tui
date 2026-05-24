package aws

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type sampleRow struct {
	Name string
	N    int
}

func init() { gob.Register([]sampleRow(nil)) }

func TestPersistentCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.gob")

	c := NewPersistentCache(path)
	c.Set("rows", []sampleRow{{"a", 1}, {"b", 2}}, time.Minute)
	c.flushToDisk()

	// New instance loading the same file should see the entry.
	c2 := NewPersistentCache(path)
	got, ok := c2.Get("rows")
	if !ok {
		t.Fatalf("expected entry to round-trip from disk")
	}
	rows, ok := got.([]sampleRow)
	if !ok {
		t.Fatalf("got %T, want []sampleRow", got)
	}
	if len(rows) != 2 || rows[0].Name != "a" || rows[1].N != 2 {
		t.Fatalf("unexpected payload after round-trip: %+v", rows)
	}
}

func TestPersistentCacheDropsExpired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.gob")

	c := NewPersistentCache(path)
	c.Set("fresh", []sampleRow{{"a", 1}}, time.Minute)
	c.Set("stale", []sampleRow{{"b", 2}}, -1*time.Second) // already expired
	c.flushToDisk()

	c2 := NewPersistentCache(path)
	if _, ok := c2.Get("stale"); ok {
		t.Fatalf("expired entry should not have been restored")
	}
	if _, ok := c2.Get("fresh"); !ok {
		t.Fatalf("fresh entry should have been restored")
	}
}

func TestPersistentCacheCorruptFileTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.gob")
	if err := os.WriteFile(path, []byte("not a gob file"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	c := NewPersistentCache(path)
	if _, ok := c.Get("anything"); ok {
		t.Fatalf("corrupt cache should return no entries")
	}
}

func TestInvalidateAllRemovesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.gob")
	c := NewPersistentCache(path)
	c.Set("rows", []sampleRow{{"a", 1}}, time.Minute)
	c.flushToDisk()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected cache file to exist, got %v", err)
	}
	c.InvalidateAll()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected cache file removed, stat err = %v", err)
	}
}
