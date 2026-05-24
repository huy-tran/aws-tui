package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestMarkerFreshness(t *testing.T) {
	dir := t.TempDir()
	// missing
	if ok, _ := markerFresh(dir); ok {
		t.Fatalf("missing marker should be not-fresh")
	}
	// fresh
	if err := writeMarker(dir, time.Now().Add(1*time.Hour)); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}
	if ok, _ := markerFresh(dir); !ok {
		t.Fatalf("marker 1h in the future should be fresh")
	}
	// expired
	if err := writeMarker(dir, time.Now().Add(-1*time.Hour)); err != nil {
		t.Fatalf("writeMarker (past): %v", err)
	}
	if ok, _ := markerFresh(dir); ok {
		t.Fatalf("marker 1h in the past should not be fresh")
	}
	// corrupt -> not fresh, no panic
	if err := os.WriteFile(filepath.Join(dir, "unlock.marker"), []byte("not a timestamp"), 0o600); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}
	if ok, _ := markerFresh(dir); ok {
		t.Fatalf("corrupt marker should not be fresh")
	}
}

func TestBackupCodeFormat(t *testing.T) {
	cases := map[string]bool{
		"ABCDE-FGHJK": true,
		"abcde-fghjk": true, // case-insensitive
		"ABCDEFGHJK":  false,
		"ABCD-FGHJK":  false,
		"":            false,
		"123456":      false, // looks like a TOTP code
	}
	for in, want := range cases {
		if got := isBackupCodeFormat(in); got != want {
			t.Fatalf("isBackupCodeFormat(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBackupCodeConsumeOnce(t *testing.T) {
	dir := t.TempDir()
	codes, err := generateAndSaveBackupCodes(dir)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("expected 10 codes, got %d", len(codes))
	}

	// First use consumes successfully.
	ok, err := consumeBackupCode(dir, codes[0])
	if err != nil || !ok {
		t.Fatalf("first consume failed: ok=%v err=%v", ok, err)
	}
	// Same code rejected the second time.
	ok, err = consumeBackupCode(dir, codes[0])
	if err != nil {
		t.Fatalf("second consume err: %v", err)
	}
	if ok {
		t.Fatalf("backup code should be one-shot, second use was accepted")
	}
	// The other 9 still work.
	for i := 1; i < 10; i++ {
		ok, err := consumeBackupCode(dir, codes[i])
		if err != nil || !ok {
			t.Fatalf("code %d should still be usable: ok=%v err=%v", i, ok, err)
		}
	}
	// All consumed -> empty list.
	b, err := os.ReadFile(filepath.Join(dir, "backup.codes"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var hashes []string
	if err := json.Unmarshal(b, &hashes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(hashes) != 0 {
		t.Fatalf("expected empty after consuming all, got %d", len(hashes))
	}
}

func TestBackupCodeRejectsUnknown(t *testing.T) {
	dir := t.TempDir()
	if _, err := generateAndSaveBackupCodes(dir); err != nil {
		t.Fatalf("generate: %v", err)
	}
	ok, err := consumeBackupCode(dir, "ZZZZZ-ZZZZZ")
	if err != nil {
		t.Fatalf("err on unknown: %v", err)
	}
	if ok {
		t.Fatalf("unknown code should be rejected")
	}
}

func TestNextBackoffSchedule(t *testing.T) {
	cases := []struct {
		prev time.Duration
		next time.Duration
	}{
		{0, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{4 * time.Second, 8 * time.Second},
		{8 * time.Second, 16 * time.Second},
		{16 * time.Second, 30 * time.Second}, // cap
		{30 * time.Second, 30 * time.Second}, // stays capped
	}
	for _, c := range cases {
		got := nextBackoff(c.prev)
		if got != c.next {
			t.Fatalf("nextBackoff(%v) = %v, want %v", c.prev, got, c.next)
		}
	}
}

func TestSaveAndLoadSecret(t *testing.T) {
	dir := t.TempDir()
	const s = "JBSWY3DPEHPK3PXP"
	if err := saveSecret(dir, s); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadSecret(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != s {
		t.Fatalf("round-trip mismatch: %q != %q", got, s)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "totp.secret"))
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		// On POSIX platforms 0o600 is honored - assert no group/world bits.
		// Windows' Go runtime reports 0o666 regardless of the requested
		// mode because mode bits don't map to ACLs; skip there.
		if info.Mode()&0o077 != 0 {
			t.Fatalf("totp.secret should not be group/world readable, got mode %v", info.Mode())
		}
	}
}

func TestHashCodeIsCaseInsensitive(t *testing.T) {
	if hashCode("abcde-fghjk") != hashCode("ABCDE-FGHJK") {
		t.Fatalf("hashCode should be case-insensitive after upper-casing")
	}
}
