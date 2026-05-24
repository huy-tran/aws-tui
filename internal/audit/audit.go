// Package audit gates destructive AWS calls and records every write
// attempt (real or dry-run) to ~/.aws-tui/audit.log as a JSON line.
//
// Views consult IsDryRun() before issuing a write; when true they skip
// the SDK call and jump to the success-message path. Either way they
// call Log so the intent is preserved on disk.
package audit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Action captures the intent of one destructive call.
type Action struct {
	Profile string         `json:"profile"`
	Region  string         `json:"region"`
	Action  string         `json:"action"`           // e.g. "cloudfront:CreateInvalidation"
	Target  string         `json:"target"`           // resource id / arn
	Payload map[string]any `json:"payload,omitempty"`// safe-to-log metadata; never raw secrets
}

// Mode is the runtime configuration set once at startup.
type Mode struct {
	DryRun bool
}

var (
	mu      sync.RWMutex
	current Mode
)

// SetMode installs the runtime mode. Safe to call from main once before
// the Bubble Tea program starts.
func SetMode(m Mode) {
	mu.Lock()
	current = m
	mu.Unlock()
}

// IsDryRun reports whether destructive calls should be intercepted.
func IsDryRun() bool {
	mu.RLock()
	defer mu.RUnlock()
	return current.DryRun
}

// Log appends one record. result is the success identifier when known
// (invalidation id, parameter version, etc); empty for failures or
// dry-runs that didn't produce one. Best-effort: errors are swallowed
// so a failing audit write never breaks the surrounding workflow.
func Log(a Action, dryRun bool, result string) {
	path, err := logPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	record := struct {
		TS      string         `json:"ts"`
		Profile string         `json:"profile"`
		Region  string         `json:"region"`
		Action  string         `json:"action"`
		Target  string         `json:"target"`
		Payload map[string]any `json:"payload,omitempty"`
		DryRun  bool           `json:"dry_run"`
		Result  string         `json:"result,omitempty"`
	}{
		TS:      time.Now().UTC().Format(time.RFC3339),
		Profile: a.Profile,
		Region:  a.Region,
		Action:  a.Action,
		Target:  a.Target,
		Payload: a.Payload,
		DryRun:  dryRun,
		Result:  result,
	}
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// logPath returns ~/.aws-tui/audit.log resolved against the user's home.
func logPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("empty home directory")
	}
	return filepath.Join(home, ".aws-tui", "audit.log"), nil
}
