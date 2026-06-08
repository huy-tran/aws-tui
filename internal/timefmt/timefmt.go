// Package timefmt centralises how the app renders timestamps for display.
// Every user-facing timestamp is shown in the local timezone with the zone
// abbreviation appended (e.g. "2026-06-08 15:04:05 AEST") so it is never
// ambiguous about whether a time is local or UTC.
package timefmt

import "time"

// Common display layouts. Pass one of these (or any reference layout) to Zone.
const (
	DateTime = "2006-01-02 15:04:05"
	DateHM   = "2006-01-02 15:04"
	Clock    = "15:04:05"
)

// Zone formats t in the local timezone using layout, with the local zone
// abbreviation appended. Callers are responsible for their own zero/nil
// placeholder handling before calling this.
func Zone(t time.Time, layout string) string {
	return t.Local().Format(layout + " MST")
}
