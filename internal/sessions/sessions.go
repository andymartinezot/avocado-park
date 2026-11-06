// Package sessions builds the list of agent sessions shown by avocado-park:
// live Claude Code and Codex agents from herdr, enriched with Claude's own
// session files and memory use, plus the sessions parked earlier.
package sessions

import (
	"cmp"
	"path/filepath"
	"slices"
	"time"

	"github.com/andymartinezot/avocado-park/internal/herdr"
	"github.com/andymartinezot/avocado-park/internal/state"
)

// AgentKind is the default herdr agent kind, also assumed for parked entries
// written before other kinds were supported.
const AgentKind = "claude"

// Agent describes how to exit and resume one kind of agent.
type Agent struct {
	// Exit is the prompt that quits the agent.
	Exit string
	// Resume returns the agent's arguments that resume session id.
	Resume func(id string) []string
}

// Agents are the herdr agent kinds avocado-park manages.
var Agents = map[string]Agent{
	"claude": {Exit: "/exit", Resume: func(id string) []string { return []string{"--resume", id} }},
	"codex":  {Exit: "/quit", Resume: func(id string) []string { return []string{"resume", id} }},
	// OpenCode's TUI quits on /exit and reopens a session with --session.
	"opencode": {Exit: "/exit", Resume: func(id string) []string { return []string{"--session", id} }},
}

// Status values. Live statuses come straight from herdr.
const (
	StatusWorking = "working"
	StatusIdle    = "idle"
	StatusDone    = "done"
	StatusBlocked = "blocked"
	StatusUnknown = "unknown"
	StatusParked  = "parked"
)

// Row is one session.
type Row struct {
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	SessionID   string    `json:"session_id"`
	PaneID      string    `json:"pane_id"`
	TabID       string    `json:"tab_id"`
	WorkspaceID string    `json:"workspace_id"`
	Workspace   string    `json:"workspace"`
	Tab         string    `json:"tab,omitempty"`        // the tab's label
	PaneLabel   string    `json:"pane_label,omitempty"` // the name given to the pane, if any
	Cwd         string    `json:"cwd"`
	PID         int       `json:"pid,omitempty"`
	RSSBytes    int64     `json:"rss_bytes,omitempty"`
	LastActive  time.Time `json:"last_active,omitzero"`
	ParkedAt    time.Time `json:"parked_at,omitzero"`
	Focused     bool      `json:"focused,omitempty"`
	// FreedBytes is, for a parked session, the memory it used when parked.
	FreedBytes int64 `json:"freed_bytes,omitempty"`
	// AgentRef is the session id herdr reported for the pane when listed. It
	// can differ from SessionID (read from Claude's session file) after a
	// /resume or /clear inside Claude, and is what Park compares against.
	AgentRef string `json:"-"`
	// PaneExists is false for a parked session whose pane was closed.
	PaneExists bool `json:"pane_exists"`
	Pending    bool `json:"pending,omitempty"`
}

// Parked reports whether the row is a parked session.
func (r Row) Parked() bool { return r.Status == StatusParked }

// Ready reports whether the agent is waiting for input and safe to park.
func (r Row) Ready() bool { return r.Status == StatusIdle || r.Status == StatusDone }

// IdleFor is how long the session has been inactive (zero if unknown).
func (r Row) IdleFor(now time.Time) time.Duration {
	if r.Status == StatusWorking || r.LastActive.IsZero() {
		return 0
	}
	return now.Sub(r.LastActive)
}

// PendingGrace is how long a park that is still waiting for its agent to exit
// keeps its entry while the session is live. After that the park is taken as
// abandoned and the session as running.
const PendingGrace = 10 * time.Minute

// Herdr is the part of the herdr API needed to list sessions.
type Herdr interface {
	Agents() ([]herdr.Pane, error)
	Panes() ([]herdr.Pane, error)
	Workspaces() ([]herdr.Workspace, error)
	Tabs() ([]herdr.Tab, error)
	ForegroundProcesses(pane string) ([]herdr.Process, error)
}

// Inputs are the non-herdr data sources, injectable for tests.
type Inputs struct {
	ClaudeDir string
	Parked    []state.Entry
	// RSS returns resident memory in bytes for pid and its descendants.
	RSS func(pid int) int64
	Now time.Time
}

// Result is the session list plus parked entries that are live again.
type Result struct {
	Rows []Row
	// Resumed lists parked session ids that are running again (for example
	// resumed by hand), so the caller can drop them from the state file.
	Resumed []string
}

// Build lists live Claude sessions and parked ones, sorted for display.
func Build(h Herdr, in Inputs) (Result, error) {
	agents, err := h.Agents()
	if err != nil {
		return Result{}, err
	}
	panes, err := h.Panes()
	if err != nil {
		return Result{}, err
	}
	workspaces, _ := h.Workspaces()
	wsLabel := map[string]string{}
	wsByLabel := map[string]string{} // label → id, for labels used once
	for _, w := range workspaces {
		wsLabel[w.WorkspaceID] = w.Label
		if _, dup := wsByLabel[w.Label]; dup {
			wsByLabel[w.Label] = ""
		} else {
			wsByLabel[w.Label] = w.WorkspaceID
		}
	}
	tabs, _ := h.Tabs()
	tabLabel := map[string]string{}
	for _, t := range tabs {
		tabLabel[t.TabID] = t.Label
	}
	paneByID := map[string]herdr.Pane{}
	for _, p := range panes {
		paneByID[p.PaneID] = p
	}
	files := LoadClaudeSessions(in.ClaudeDir)

	var res Result
	live := map[string]bool{}
	for _, a := range agents {
		if _, ok := Agents[a.Agent]; !ok {
			continue
		}
		row := Row{
			Name:        a.TerminalTitleStripped,
			Kind:        a.Agent,
			Status:      cmp.Or(a.AgentStatus, StatusUnknown),
			SessionID:   a.SessionID(),
			AgentRef:    a.SessionID(),
			PaneID:      a.PaneID,
			TabID:       a.TabID,
			WorkspaceID: a.WorkspaceID,
			Workspace:   wsLabel[a.WorkspaceID],
			Tab:         tabLabel[a.TabID],
			PaneLabel:   paneByID[a.PaneID].Label,
			Cwd:         cmp.Or(a.ForegroundCwd, a.Cwd),
			Focused:     a.Focused,
			PaneExists:  true,
		}
		if procs, err := h.ForegroundProcesses(a.PaneID); err == nil {
			row.PID = agentPID(procs, a.Agent)
		}
		if f, ok := files[row.PID]; ok && row.PID != 0 {
			// The session file belongs to this exact process, so it wins over
			// herdr's ref, which is only refreshed when a session starts.
			row.SessionID = cmp.Or(f.SessionID, row.SessionID)
			row.Name = cmp.Or(f.Name, row.Name)
			row.LastActive = f.UpdatedAt()
		}
		if row.Name == "" && row.Cwd != "" {
			row.Name = filepath.Base(row.Cwd)
		}
		if row.Name == "" {
			row.Name = row.Kind
		}
		if row.PID != 0 && in.RSS != nil {
			row.RSSBytes = in.RSS(row.PID)
		}
		if row.SessionID != "" {
			live[row.SessionID] = true
		}
		res.Rows = append(res.Rows, row)
	}

	for _, e := range in.Parked {
		if live[e.SessionID] {
			// A park still waiting for the agent to exit keeps its entry (the
			// live row shows the session meanwhile); anything else running
			// again was resumed some other way.
			if !e.Pending || in.Now.Sub(e.ParkedAt) > PendingGrace {
				res.Resumed = append(res.Resumed, e.SessionID)
			}
			continue
		}
		// herdr reassigns ids when it restarts: when the saved id now names a
		// workspace with another label, find the workspace by its label.
		wsID := e.WorkspaceID
		if e.Workspace != "" && wsLabel[wsID] != e.Workspace {
			wsID = wsByLabel[e.Workspace]
		}
		p, exists := paneByID[e.PaneID]
		// The saved pane id may name a pane in another workspace by now.
		if e.Workspace != "" {
			exists = exists && wsLabel[p.WorkspaceID] == e.Workspace
		} else {
			exists = exists && (e.WorkspaceID == "" || p.WorkspaceID == e.WorkspaceID)
		}
		// Once the agent is gone, a pending park is complete.
		pending := e.Pending && exists && p.Agent != ""
		// A pane that is still the session's shows its current names; for
		// anything else, the names saved when it was parked.
		tab, paneLabel := e.Tab, e.PaneLabel
		if exists && p.Agent == "" {
			tab, paneLabel = cmp.Or(tabLabel[p.TabID], tab), p.Label
		}
		res.Rows = append(res.Rows, Row{
			Name:        e.Name,
			Kind:        cmp.Or(e.Kind, AgentKind),
			Status:      StatusParked,
			SessionID:   e.SessionID,
			PaneID:      e.PaneID,
			TabID:       e.TabID,
			WorkspaceID: wsID,
			Workspace:   cmp.Or(e.Workspace, wsLabel[wsID]),
			Tab:         tab,
			PaneLabel:   paneLabel,
			Cwd:         e.Cwd,
			LastActive:  e.LastActive,
			ParkedAt:    e.ParkedAt,
			PaneExists:  exists && p.Agent == "",
			Pending:     pending,
			FreedBytes:  e.FreedBytes,
		})
	}

	Sort(res.Rows)
	return res, nil
}

// Sort orders live sessions by most recent activity, then parked sessions by
// most recently parked.
func Sort(rows []Row) {
	slices.SortStableFunc(rows, func(a, b Row) int {
		if a.Parked() != b.Parked() {
			if a.Parked() {
				return 1
			}
			return -1
		}
		if a.Parked() {
			return b.ParkedAt.Compare(a.ParkedAt)
		}
		if c := b.LastActive.Compare(a.LastActive); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
}

// agentPID finds the agent's own process among a pane's foreground ones.
func agentPID(procs []herdr.Process, kind string) int {
	for _, p := range procs {
		if filepath.Base(p.Argv0) == kind {
			return p.PID
		}
	}
	return 0
}
