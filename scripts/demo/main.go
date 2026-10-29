// Command demo runs the avocado-park panel against an in-memory herdr with
// made-up sessions, for recording the README demo (scripts/demo/demo.tape).
// Nothing in it talks to a real herdr or touches real sessions.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/andymartinezot/avocado-park/internal/herdr"
	"github.com/andymartinezot/avocado-park/internal/park"
	"github.com/andymartinezot/avocado-park/internal/state"
	"github.com/andymartinezot/avocado-park/internal/tui"
)

type session struct {
	name, kind, status, ws, tab, cwd string
	idle                             time.Duration // 0: no activity time (Codex, OpenCode)
	mb                               int64
	parked                           bool
}

var demo = []session{
	{name: "api-refactor", kind: "claude", status: "working", ws: "backend", tab: "t1", cwd: "/src/api", idle: time.Minute, mb: 460},
	{name: "codex-migration", kind: "codex", status: "unknown", ws: "backend", tab: "t1", cwd: "/src/api", mb: 142},
	{name: "release-notes", kind: "claude", status: "idle", ws: "docs", tab: "t2", cwd: "/src/docs", idle: 48 * time.Minute, mb: 267},
	{name: "changelog-bot", kind: "claude", status: "idle", ws: "docs", tab: "t2", cwd: "/src/docs", idle: 2 * time.Hour, mb: 231},
	{name: "docs-site", kind: "opencode", status: "idle", ws: "docs", tab: "t3", cwd: "/src/site", mb: 118},
	{name: "cert-renewal", kind: "claude", status: "idle", ws: "infra", tab: "t4", cwd: "/src/infra", idle: 43 * time.Hour, mb: 207},
	{name: "weekly-report", kind: "claude", status: "idle", ws: "home", tab: "t5", cwd: "/src/tickets", idle: 5 * 24 * time.Hour, mb: 185},
	{name: "helm-charts", kind: "claude", status: "idle", ws: "infra", tab: "t6", cwd: "/src/helm", idle: 9 * 24 * time.Hour, mb: 195},
	{name: "eks-cluster", kind: "claude", status: "idle", ws: "infra", tab: "t7", cwd: "/src/eks", idle: 12 * 24 * time.Hour, mb: 305, parked: true},
	{name: "chat-digest", kind: "claude", status: "idle", ws: "home", tab: "t8", cwd: "/src/slack", idle: 22 * 24 * time.Hour, mb: 207, parked: true},
}

var workspaces = map[string]string{"backend": "w1", "docs": "w2", "infra": "w3", "home": "w4"}

// fakeHerdr is just enough of herdr for the panel: panes with or without an
// agent, an agent that exits shortly after /exit, and resume.
type fakeHerdr struct {
	mu        sync.Mutex
	panes     map[string]*herdr.Pane
	order     []string
	pid       map[string]int // pane → agent pid
	exitAt    map[string]time.Time
	rss       map[int]int64
	claudeDir string
	nextPID   int
}

func (f *fakeHerdr) settle() {
	for id, at := range f.exitAt {
		if time.Now().After(at) {
			p := f.panes[id]
			p.Agent, p.AgentStatus, p.AgentSession = "", "", nil
			delete(f.pid, id)
			delete(f.exitAt, id)
		}
	}
}

func (f *fakeHerdr) Agents() ([]herdr.Pane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	var out []herdr.Pane
	for _, id := range f.order {
		if p := f.panes[id]; p.Agent != "" {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (f *fakeHerdr) Panes() ([]herdr.Pane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	var out []herdr.Pane
	for _, id := range f.order {
		out = append(out, *f.panes[id])
	}
	return out, nil
}

func (f *fakeHerdr) Workspaces() ([]herdr.Workspace, error) {
	var out []herdr.Workspace
	for label, id := range workspaces {
		out = append(out, herdr.Workspace{WorkspaceID: id, Label: label})
	}
	return out, nil
}

func (f *fakeHerdr) ForegroundProcesses(pane string) ([]herdr.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	if pid, ok := f.pid[pane]; ok {
		return []herdr.Process{{PID: pid, Argv0: f.panes[pane].Agent}}, nil
	}
	return []herdr.Process{{PID: 1, Argv0: "-zsh"}}, nil
}

func (f *fakeHerdr) Pane(id string) (herdr.Pane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	if p, ok := f.panes[id]; ok {
		return *p, nil
	}
	return herdr.Pane{}, &herdr.Error{Code: "pane_not_found", Message: id}
}

func (f *fakeHerdr) Prompt(pane, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exitAt[pane] = time.Now().Add(700 * time.Millisecond)
	return nil
}

func (f *fakeHerdr) RunInPane(string, string) error { return nil }
func (f *fakeHerdr) FocusPane(string) error         { return nil }

func (f *fakeHerdr) CreateTab(ws, cwd, label string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("%s:p%d", ws, len(f.order)+100)
	f.panes[id] = &herdr.Pane{PaneID: id, WorkspaceID: ws, TabID: id + "t", Cwd: cwd}
	f.order = append(f.order, id)
	return id, nil
}

func (f *fakeHerdr) StartAgent(name, kind, pane string, args []string, _ time.Duration) error {
	time.Sleep(900 * time.Millisecond) // the agent loading the conversation
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.panes[pane]
	id := args[len(args)-1]
	f.nextPID++
	p.Agent, p.AgentStatus = kind, "idle"
	p.AgentSession = &herdr.AgentSession{Kind: "id", Value: id}
	f.pid[pane] = f.nextPID
	f.rss[f.nextPID] = 240 << 20
	writeSession(f.claudeDir, f.nextPID, id, p.TerminalTitleStripped, time.Now())
	return nil
}

func (f *fakeHerdr) alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settle()
	for _, p := range f.pid {
		if p == pid {
			return true
		}
	}
	return false
}

func writeSession(dir string, pid int, id, name string, at time.Time) {
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": id, "name": name, "updatedAt": at.UnixMilli()})
	_ = os.WriteFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"), b, 0o600)
}

func main() {
	print := flag.Bool("print", false, "print one frame and exit")
	flag.Parse()
	tmp, err := os.MkdirTemp("", "avocado-park-demo")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)
	claudeDir := filepath.Join(tmp, "claude")
	_ = os.MkdirAll(filepath.Join(claudeDir, "sessions"), 0o700)
	store := state.Store{Path: filepath.Join(tmp, "state", "parked.json")}

	f := &fakeHerdr{panes: map[string]*herdr.Pane{}, pid: map[string]int{}, exitAt: map[string]time.Time{},
		rss: map[int]int64{}, claudeDir: claudeDir, nextPID: 1000}
	now := time.Now()
	var self string
	for i, s := range demo {
		ws := workspaces[s.ws]
		pane := fmt.Sprintf("%s:p%d", ws, i+1)
		sid := fmt.Sprintf("%08x-demo-%04d", 0x3a5f0000+i*7919, i)
		p := &herdr.Pane{PaneID: pane, TabID: ws + ":" + s.tab, WorkspaceID: ws, Cwd: s.cwd, ForegroundCwd: s.cwd,
			TerminalTitleStripped: s.name}
		f.panes[pane] = p
		f.order = append(f.order, pane)
		if s.parked {
			_ = store.Put(state.Entry{SessionID: sid, Name: s.name, Kind: s.kind, PaneID: pane, TabID: p.TabID,
				WorkspaceID: ws, Workspace: s.ws, Cwd: s.cwd, ParkedAt: now.Add(-s.idle / 2),
				LastActive: now.Add(-s.idle), FreedBytes: s.mb << 20})
			continue
		}
		p.Agent, p.AgentStatus = s.kind, s.status
		p.AgentSession = &herdr.AgentSession{Kind: "id", Value: sid}
		pid := 100 + i
		f.pid[pane], f.rss[pid] = pid, s.mb<<20
		if s.kind == "claude" {
			writeSession(claudeDir, pid, sid, s.name, now.Add(-s.idle))
		}
		if self == "" {
			self = pane // the session you're typing in
		}
	}

	p := &park.Parker{
		H: f, Store: store, ClaudeDir: claudeDir,
		RSS: func() func(int) int64 {
			f.mu.Lock()
			defer f.mu.Unlock()
			m := map[int]int64{}
			for k, v := range f.rss {
				m[k] = v
			}
			return func(pid int) int64 { return m[pid] }
		},
		Alive: f.alive, Now: time.Now, Sleep: time.Sleep,
		ExitTimeout: 10 * time.Second, StartTimeout: 10 * time.Second,
	}
	opt := tui.Options{Self: self, IdleDays: 3, Refresh: time.Second}
	if *print {
		frame, err := tui.Render(p, opt, 100, 22)
		fmt.Println(frame)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return
	}
	if err := tui.Run(p, opt); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
