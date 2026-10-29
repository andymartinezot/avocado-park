// Package state stores parked sessions in a small JSON file.
//
// The CLI, the herdr plugin and a future menu bar app all run as separate
// processes, so the file is guarded by an advisory lock and written atomically.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Entry is one parked session.
type Entry struct {
	SessionID   string    `json:"session_id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind,omitempty"` // herdr agent kind; empty means claude
	PaneID      string    `json:"pane_id"`
	TabID       string    `json:"tab_id"`
	WorkspaceID string    `json:"workspace_id"`
	Cwd         string    `json:"cwd"`
	ParkedAt    time.Time `json:"parked_at"`
	LastActive  time.Time `json:"last_active,omitzero"`
	// Workspace is the workspace's label. herdr reassigns ids when it
	// restarts; the label is how the workspace is found again.
	Workspace string `json:"workspace,omitempty"`
	// FreedBytes is the memory the session used when it was parked.
	FreedBytes int64 `json:"freed_bytes,omitempty"`
	// Pending means /exit was sent but the agent was not seen exiting.
	Pending bool `json:"pending,omitempty"`
}

// Store is the parked-session file.
type Store struct{ Path string }

// DefaultPath is $AVOCADO_PARK_STATE_DIR, else $XDG_STATE_HOME/avocado-park, else
// ~/.local/state/avocado-park. It deliberately ignores HERDR_PLUGIN_STATE_DIR so
// that the plugin and the standalone CLI always share one file.
func DefaultPath() string {
	dir := os.Getenv("AVOCADO_PARK_STATE_DIR")
	if dir == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".local", "state")
		}
		dir = filepath.Join(base, "avocado-park")
		migrate(filepath.Join(base, "herdr-park"), dir)
	}
	return filepath.Join(dir, "parked.json")
}

// migrate moves the state directory of releases named herdr-park to its
// new name, once: only when the new directory doesn't exist yet.
func migrate(old, dir string) {
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		return
	}
	if fi, err := os.Lstat(old); err == nil && fi.IsDir() {
		_ = os.Rename(old, dir)
	}
}

// Load reads every entry. A missing file is an empty list.
func (s Store) Load() ([]Entry, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []Entry
	if len(b) == 0 {
		return nil, nil
	}
	return entries, json.Unmarshal(b, &entries)
}

// Update runs fn on the entries under an exclusive lock and saves the result.
func (s Store) Update(fn func([]Entry) ([]Entry, error)) error {
	if err := s.EnsureDir(); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	entries, err := s.Load()
	if err != nil {
		return err
	}
	if entries, err = fn(entries); err != nil {
		return err
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	// A fresh temp file (0600, O_EXCL): never follows a planted symlink.
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), filepath.Base(s.Path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

// EnsureDir creates the state directory, and makes it private even when it
// already existed with wider permissions.
func (s Store) EnsureDir() error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// Lock takes an exclusive lock that serializes park and unpark across the
// panel, the CLI and herdr hooks, so two of them never act on one session at
// the same time. It blocks until the lock is free; call the returned func to
// release it.
func (s Store) Lock() (func(), error) {
	if err := s.EnsureDir(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.Path+".action.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// Put adds or replaces the entry for e.SessionID.
func (s Store) Put(e Entry) error {
	return s.Update(func(entries []Entry) ([]Entry, error) {
		return append(Without(entries, e.SessionID), e), nil
	})
}

// Remove deletes the entries for the given session ids.
func (s Store) Remove(sessionIDs ...string) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	return s.Update(func(entries []Entry) ([]Entry, error) {
		for _, id := range sessionIDs {
			entries = Without(entries, id)
		}
		return entries, nil
	})
}

// Without returns entries minus the one with the given session id.
func Without(entries []Entry, sessionID string) []Entry {
	out := entries[:0:0]
	for _, e := range entries {
		if e.SessionID != sessionID {
			out = append(out, e)
		}
	}
	return out
}
