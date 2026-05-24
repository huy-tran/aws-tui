package state

import "testing"

func TestBookmarkAddIsIdempotent(t *testing.T) {
	s := &Store{Bookmarks: map[string][]Bookmark{}}
	b := Bookmark{Tab: "EC2", Region: "us-east-1", ID: "i-1", Label: "web"}
	if !s.AddBookmark("prod", b) {
		t.Fatalf("first add should report new")
	}
	if s.AddBookmark("prod", b) {
		t.Fatalf("second add of identical (tab, id) should report duplicate")
	}
	if got := len(s.Bookmarks["prod"]); got != 1 {
		t.Fatalf("expected 1 entry after duplicate add, got %d", got)
	}
}

func TestBookmarkPerProfileIsolation(t *testing.T) {
	s := &Store{Bookmarks: map[string][]Bookmark{}}
	s.AddBookmark("prod", Bookmark{Tab: "EC2", ID: "i-1"})
	s.AddBookmark("dev", Bookmark{Tab: "EC2", ID: "i-1"})
	if len(s.ListBookmarks("prod")) != 1 || len(s.ListBookmarks("dev")) != 1 {
		t.Fatalf("each profile should hold its own bookmark")
	}
	s.RemoveBookmark("prod", "EC2", "i-1")
	if len(s.ListBookmarks("prod")) != 0 {
		t.Fatalf("remove failed on prod")
	}
	if len(s.ListBookmarks("dev")) != 1 {
		t.Fatalf("dev should be unaffected by prod's removal")
	}
}

func TestIsBookmarked(t *testing.T) {
	s := &Store{Bookmarks: map[string][]Bookmark{}}
	if s.IsBookmarked("prod", "EC2", "i-1") {
		t.Fatalf("nothing bookmarked yet")
	}
	s.AddBookmark("prod", Bookmark{Tab: "EC2", ID: "i-1"})
	if !s.IsBookmarked("prod", "EC2", "i-1") {
		t.Fatalf("expected bookmark to be detected")
	}
	if s.IsBookmarked("prod", "EC2", "i-2") {
		t.Fatalf("different id should not match")
	}
	if s.IsBookmarked("prod", "S3", "i-1") {
		t.Fatalf("different tab should not match")
	}
}

func TestRemoveNonExistentReportsFalse(t *testing.T) {
	s := &Store{Bookmarks: map[string][]Bookmark{}}
	if s.RemoveBookmark("prod", "EC2", "i-missing") {
		t.Fatalf("removing missing should report false, not panic")
	}
}
