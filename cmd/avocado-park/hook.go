package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/andymartinezot/avocado-park/internal/config"
	"github.com/andymartinezot/avocado-park/internal/herdr"
	"github.com/andymartinezot/avocado-park/internal/park"
	"github.com/andymartinezot/avocado-park/internal/state"
)

// autoParkEvery limits how often a hook may run auto-park.
const autoParkEvery = time.Hour

// runHook handles a herdr event hook. It runs on every pane focus, so the
// common case (nothing parked there, auto-park off or recently done) reads
// two small files and exits.
func runHook(cfg config.Config, c *herdr.Client, p *park.Parker) error {
	pane := eventPane(os.Getenv("HERDR_PLUGIN_EVENT_JSON"))
	if pane == "" {
		pane = focusedPane()
	}
	// The manifest says pane.focused; herdr's event stream spells it pane_focused.
	if ev := os.Getenv("HERDR_PLUGIN_EVENT"); (ev == "pane.focused" || ev == "pane_focused") && pane != "" {
		remindParked(c, p.Store, pane)
	}
	if cfg.AutoParkDays > 0 {
		return autoPark(c, p, cfg.AutoParkDays, pane)
	}
	return nil
}

// eventPane returns the first "pane_id" found in an event payload, nearest
// the top level first, so it works whether herdr nests the event or not.
func eventPane(raw string) string {
	var v any
	if raw == "" || json.Unmarshal([]byte(raw), &v) != nil {
		return ""
	}
	level := []any{v}
	for depth := 0; depth < 4 && len(level) > 0; depth++ {
		var next []any
		for _, n := range level {
			obj, ok := n.(map[string]any)
			if !ok {
				continue
			}
			if id, ok := obj["pane_id"].(string); ok && id != "" {
				return id
			}
			for _, child := range obj {
				next = append(next, child)
			}
		}
		level = next
	}
	return ""
}

// remindParked shows a toast when the focused pane holds a parked session,
// so a plain shell there isn't mistaken for a finished one.
func remindParked(c *herdr.Client, store state.Store, pane string) {
	entries, err := store.Load()
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.PaneID != pane {
			continue
		}
		if info, err := c.Pane(pane); err != nil || info.Agent != "" {
			return // closed, or something runs there again
		}
		_ = c.Notify(e.Name+" is parked",
			"Resume it here with u in the avocado-park panel, or the \"Park: resume this session\" action.")
		return
	}
}

// autoPark parks sessions idle for days or more, except the one in pane, at
// most once per autoParkEvery. A lock keeps two hooks from running it at once.
func autoPark(c *herdr.Client, p *park.Parker, days int, pane string) error {
	dir := filepath.Dir(p.Store.Path)
	if err := p.Store.EnsureDir(); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "auto-park.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil // another hook is running it
		}
		return err
	}
	stamp := filepath.Join(dir, "auto-park.last")
	if fi, err := os.Stat(stamp); err == nil && p.Now().Sub(fi.ModTime()) < autoParkEvery {
		return nil
	}
	if err := os.WriteFile(stamp, nil, 0o600); err != nil {
		return err
	}
	now := p.Now()
	if err := os.Chtimes(stamp, now, now); err != nil {
		return err
	}

	rows, err := p.List()
	if err != nil {
		return err
	}
	batch := park.Idle(rows, now, time.Duration(days)*24*time.Hour, pane)
	if len(batch) == 0 {
		return nil
	}
	names := make([]string, len(batch))
	for i, r := range batch {
		names[i] = r.Name
	}
	n, err := p.ParkAll(batch)
	if n > 0 {
		_ = c.Notify(fmt.Sprintf("Auto-parked %d sessions idle %dd+", n, days), strings.Join(names, ", "))
	}
	return err
}
