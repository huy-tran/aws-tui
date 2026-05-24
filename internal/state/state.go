package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type Bookmark struct {
	Tab     string `json:"tab"`
	Region  string `json:"region"`
	ID      string `json:"id"`
	Label   string `json:"label"`
	AddedAt string `json:"added_at"`
}

type Store struct {
	mu sync.Mutex

	LastProfile   string                `json:"last_profile"`
	LastRegions   map[string]string     `json:"last_regions"`   // profile -> region
	BucketRegions map[string]string     `json:"bucket_regions"` // bucketName -> region
	Bookmarks     map[string][]Bookmark `json:"bookmarks"`      // profile -> bookmarks
	UpdatedAt     string                `json:"updated_at"`
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
		Bookmarks:     map[string][]Bookmark{},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, s); err != nil {
		// Corrupt file: start fresh rather than blocking startup.
		return &Store{
			LastRegions:   map[string]string{},
			BucketRegions: map[string]string{},
			Bookmarks:     map[string][]Bookmark{},
		}, nil
	}
	if s.LastRegions == nil {
		s.LastRegions = map[string]string{}
	}
	if s.BucketRegions == nil {
		s.BucketRegions = map[string]string{}
	}
	if s.Bookmarks == nil {
		s.Bookmarks = map[string][]Bookmark{}
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

	// Atomic write: tmp file + rename.
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

// AddBookmark appends a bookmark to the given profile, deduplicated by
// (tab, id). Returns true when a new entry was added; false when an
// existing one was found (in which case nothing changes - callers can
// treat that as "already bookmarked" for toggle semantics).
func (s *Store) AddBookmark(profile string, b Bookmark) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Bookmarks == nil {
		s.Bookmarks = map[string][]Bookmark{}
	}
	for _, existing := range s.Bookmarks[profile] {
		if existing.Tab == b.Tab && existing.ID == b.ID {
			return false
		}
	}
	s.Bookmarks[profile] = append(s.Bookmarks[profile], b)
	return true
}

// RemoveBookmark removes the (tab, id) entry for the profile. Returns
// true when something was actually removed.
func (s *Store) RemoveBookmark(profile, tab, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.Bookmarks[profile]
	for i, b := range list {
		if b.Tab == tab && b.ID == id {
			s.Bookmarks[profile] = append(list[:i], list[i+1:]...)
			return true
		}
	}
	return false
}

// ListBookmarks returns a copy of the bookmark slice for the profile
// so callers can iterate without holding the lock.
func (s *Store) ListBookmarks(profile string) []Bookmark {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.Bookmarks[profile]
	out := make([]Bookmark, len(src))
	copy(out, src)
	return out
}

// IsBookmarked reports whether a (tab, id) pair is currently bookmarked
// for the profile. Used by views that want to render a marker next to
// already-bookmarked rows in the future.
func (s *Store) IsBookmarked(profile, tab, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.Bookmarks[profile] {
		if b.Tab == tab && b.ID == id {
			return true
		}
	}
	return false
}
