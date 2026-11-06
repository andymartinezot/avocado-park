package park

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andymartinezot/avocado-park/internal/herdr"
	"github.com/andymartinezot/avocado-park/internal/sessions"
	"github.com/andymartinezot/avocado-park/internal/state"
)

// fake is an in-memory herdr. Sending /exit to an agent pane turns it into a
// plain shell unless stuck is set.
type fake struct {
	panes     map[string]herdr.Pane
	calls     []string
	stuck     bool
	promptErr error
	startErr  []error // consumed one per StartAgent call
	alive     map[int]bool
	newTab    string
	procs     map[string][]herdr.Process
}

func (f *fake) Agents() ([]herdr.Pane, error) {
	var out []herdr.Pane
	for _, p := range f.panes {
		if p.Agent != "" {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fake) Panes() ([]herdr.Pane, error) {
	var out []herdr.Pane
	for _, p := range f.panes {
		out = append(out, p)
	}
	return out, nil
}
func (f *fake) Workspaces() ([]herdr.Workspace, error) { return nil, nil }
func (f *fake) Tabs() ([]herdr.Tab, error)             { return nil, nil }
func (f *fake) ForegroundProcesses(p string) ([]herdr.Process, error) {
	if procs, ok := f.procs[p]; ok {
		return procs, nil
	}
	return []herdr.Process{{PID: 1, Argv0: "-zsh"}}, nil // a shell prompt
}
func (f *fake) FocusPane(p string) error { f.calls = append(f.calls, "focus "+p); return nil }
func (f *fake) ClosePane(p string) error {
	f.calls = append(f.calls, "close "+p)
	delete(f.panes, p)
	return nil
}
func (f *fake) RunInPane(p, line string) error {
	f.calls = append(f.calls, "run "+p+" "+line)
	return nil
}
func (f *fake) CreateTab(ws, cwd, label string) (string, error) {
	f.calls = append(f.calls, "tab "+ws+" "+cwd)
	f.panes[f.newTab] = herdr.Pane{PaneID: f.newTab, WorkspaceID: ws, ForegroundCwd: cwd}
	return f.newTab, nil
}
func (f *fake) Pane(id string) (herdr.Pane, error) {
	p, ok := f.panes[id]
	if !ok {
		return p, &herdr.Error{Code: "pane_not_found"}
	}
	return p, nil
}
func (f *fake) Prompt(target, text string) error {
	f.calls = append(f.calls, "prompt "+target+" "+text)
	if f.promptErr != nil {
		return f.promptErr
	}
	if !f.stuck {
		p := f.panes[target]
		p.Agent, p.AgentStatus, p.AgentSession = "", "unknown", nil
		f.panes[target] = p
		f.alive = map[int]bool{}
	}
	return nil
}
func (f *fake) StartAgent(name, kind, pane string, args []string, _ time.Duration) error {
	f.calls = append(f.calls, "start "+name+" "+kind+" "+pane+" "+strings.Join(args, " "))
	if len(f.startErr) > 0 {
		err := f.startErr[0]
		f.startErr = f.startErr[1:]
		return err
	}
	return nil
}

func newParker(t *testing.T, f *fake) (*Parker, state.Store) {
	t.Helper()
	store := state.Store{Path: filepath.Join(t.TempDir(), "parked.json")}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	p := &Parker{
		H: f, Store: store,
		Alive: func(pid int) bool { return f.alive[pid] },
		Now:   func() time.Time { return now },
		// Each sleep advances the clock, so timeouts run instantly.
		Sleep:        func(d time.Duration) { now = now.Add(d) },
		ExitTimeout:  2 * time.Second,
		StartTimeout: time.Second,
	}
	return p, store
}

func liveRow() sessions.Row {
	return sessions.Row{Name: "deploy-session", Status: "idle", SessionID: "036f9671-aaaa", AgentRef: "036f9671-aaaa", PaneID: "w3:pZ",
		WorkspaceID: "w3", Cwd: "/repo", PID: 99, PaneExists: true}
}

func agentPane() herdr.Pane {
	return herdr.Pane{PaneID: "w3:pZ", WorkspaceID: "w3", Agent: "claude", AgentStatus: "idle",
		AgentSession: &herdr.AgentSession{Kind: "id", Value: "036f9671-aaaa"}}
}

func TestParkRefusals(t *testing.T) {
	cases := map[string]func(r *sessions.Row){
		"working":    func(r *sessions.Row) { r.Status = "working" },
		"blocked":    func(r *sessions.Row) { r.Status = "blocked" },
		"no session": func(r *sessions.Row) { r.SessionID = "" },
		"parked":     func(r *sessions.Row) { r.Status = sessions.StatusParked },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fake{panes: map[string]herdr.Pane{"w3:pZ": agentPane()}}
			p, store := newParker(t, f)
			r := liveRow()
			mutate(&r)
			if err := p.Park(r, ParkOptions{}); err == nil {
				t.Fatal("Park succeeded, want refusal")
			}
			if len(f.calls) != 0 {
				t.Errorf("herdr was called: %v", f.calls)
			}
			if e, _ := store.Load(); len(e) != 0 {
				t.Errorf("state written: %+v", e)
			}
		})
	}
}

func TestParkForceWorking(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": agentPane()}}
	p, _ := newParker(t, f)
	r := liveRow()
	r.Status = "working"
	if err := p.Park(r, ParkOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
}

func TestParkSuccess(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": agentPane()}, alive: map[int]bool{99: true}}
	p, store := newParker(t, f)
	if err := p.Park(liveRow(), ParkOptions{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls, "prompt w3:pZ /exit") {
		t.Errorf("calls = %v", f.calls)
	}
	e, _ := store.Load()
	if len(e) != 1 || e[0].SessionID != "036f9671-aaaa" || e[0].Pending || e[0].Cwd != "/repo" {
		t.Fatalf("state = %+v", e)
	}

	// The parked session now lists as parked in its (shell) pane.
	rows, err := p.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Parked() || !rows[0].PaneExists {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestParkTimeoutStaysPending(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": agentPane()}, stuck: true}
	p, store := newParker(t, f)
	err := p.Park(liveRow(), ParkOptions{})
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("err = %v", err)
	}
	if e, _ := store.Load(); len(e) != 1 || !e[0].Pending {
		t.Fatalf("state = %+v", e)
	}
}

func TestParkPromptFailureForgets(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": agentPane()}, promptErr: errors.New("agent_blocked")}
	p, store := newParker(t, f)
	if err := p.Park(liveRow(), ParkOptions{}); err == nil {
		t.Fatal("want error")
	}
	if e, _ := store.Load(); len(e) != 0 {
		t.Fatalf("state = %+v", e)
	}
}

func parkedRow(paneExists bool) sessions.Row {
	r := liveRow()
	r.Status, r.PaneExists, r.PID = sessions.StatusParked, paneExists, 0
	return r
}

// parkedEntry is the state entry for parkedRow.
func parkedEntry() state.Entry {
	return state.Entry{SessionID: "036f9671-aaaa", Name: "deploy-session", PaneID: "w3:pZ", WorkspaceID: "w3", Cwd: "/repo"}
}

func TestUnparkSamePane(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w3", ForegroundCwd: "/elsewhere"}}}
	p, store := newParker(t, f)
	_ = store.Put(state.Entry{SessionID: "036f9671-aaaa", PaneID: "w3:pZ"})
	_ = p.Store.Put(parkedEntry())
	pane, err := p.Unpark(parkedRow(true))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run w3:pZ cd '/repo'", "start deploy-session claude w3:pZ --resume 036f9671-aaaa"}
	if pane != "w3:pZ" || !slices.Equal(f.calls, want) {
		t.Errorf("pane=%s calls=%v", pane, f.calls)
	}
	if e, _ := store.Load(); len(e) != 0 {
		t.Errorf("entry not removed: %+v", e)
	}
}

func TestUnparkClosedPaneOpensTab(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{}, newTab: "w3:p50"}
	p, _ := newParker(t, f)
	_ = p.Store.Put(parkedEntry())
	pane, err := p.Unpark(parkedRow(false))
	if err != nil {
		t.Fatal(err)
	}
	if pane != "w3:p50" || f.calls[0] != "tab w3 /repo" {
		t.Errorf("pane=%s calls=%v", pane, f.calls)
	}
}

func TestUnparkNameTakenRetries(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w3", ForegroundCwd: "/repo"}},
		startErr: []error{errors.New("agent name already in use")}}
	p, store := newParker(t, f)
	_ = store.Put(state.Entry{SessionID: "036f9671-aaaa"})
	_ = p.Store.Put(parkedEntry())
	if _, err := p.Unpark(parkedRow(true)); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[len(f.calls)-1]; !strings.HasPrefix(got, "start deploy-session-036f ") {
		t.Errorf("retry call = %q", got)
	}
}

func TestFind(t *testing.T) {
	rows := []sessions.Row{
		{Name: "main-session", PaneID: "w3:p1", SessionID: "6356f9b2-xx", Status: "idle"},
		{Name: "main-session", PaneID: "w2:p9", SessionID: "11111111-yy", Status: "idle"},
		{Name: "deploy", PaneID: "w3:pZ", SessionID: "036f9671-zz", Status: sessions.StatusParked},
	}
	for target, want := range map[string]string{"w3:p1": "w3:p1", "6356f9b2": "w3:p1", "deploy": "w3:pZ", "w3:pZ": "w3:pZ"} {
		r, err := Find(rows, target)
		if err != nil || r.PaneID != want {
			t.Errorf("Find(%q) = %s, %v; want %s", target, r.PaneID, err, want)
		}
	}
	if _, err := Find(rows, "main-session"); err == nil || !strings.Contains(err.Error(), "2 sessions") {
		t.Errorf("ambiguous name: err = %v", err)
	}
	if _, err := Find(rows, "6356"); err == nil {
		t.Error("short id prefix should not match")
	}
}

func TestAgentName(t *testing.T) {
	for in, want := range map[string]string{
		"laptop-cleanup-session":                "laptop-cleanup-session",
		"PROJ-1234 resume handoff":              "proj-1234-resume-handoff",
		"83-thing":                              "claude-83-thing",
		"a-very-long-session-name-that-goes-on": "a-very-long-session-name-that-go",
		"":                                      "claude-",
	} {
		if got := AgentName(in); got != want {
			t.Errorf("AgentName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIdle(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	day := 24 * time.Hour
	rows := []sessions.Row{
		{Name: "old", Status: sessions.StatusIdle, SessionID: "a", PaneID: "w1:p1", LastActive: ago(4 * day)},
		{Name: "done", Status: sessions.StatusDone, SessionID: "b", PaneID: "w1:p2", LastActive: ago(3 * day)},
		{Name: "recent", Status: sessions.StatusIdle, SessionID: "c", PaneID: "w1:p3", LastActive: ago(2 * day)},
		{Name: "blocked", Status: sessions.StatusBlocked, SessionID: "d", PaneID: "w1:p4", LastActive: ago(9 * day)},
		{Name: "self", Status: sessions.StatusIdle, SessionID: "e", PaneID: "w1:p5", LastActive: ago(9 * day)},
		{Name: "no id", Status: sessions.StatusIdle, PaneID: "w1:p6", LastActive: ago(9 * day)},
		{Name: "parked", Status: sessions.StatusParked, SessionID: "f", PaneID: "w1:p7", LastActive: ago(9 * day)},
	}
	var got []string
	for _, r := range Idle(rows, now, 3*day, "w1:p5") {
		got = append(got, r.Name)
	}
	if want := []string{"old", "done"}; !slices.Equal(got, want) {
		t.Errorf("Idle = %v, want %v", got, want)
	}
}

func TestResumable(t *testing.T) {
	rows := []sessions.Row{
		{Name: "live", Status: sessions.StatusIdle},
		{Name: "parked", Status: sessions.StatusParked},
		{Name: "pending", Status: sessions.StatusParked, Pending: true},
		{Name: "parked2", Status: sessions.StatusParked},
	}
	var got []string
	for _, r := range Resumable(rows) {
		got = append(got, r.Name)
	}
	if want := []string{"parked", "parked2"}; !slices.Equal(got, want) {
		t.Errorf("Resumable = %v, want %v", got, want)
	}
}

// A row from a few seconds ago must not exit a pane that changed since.
func TestParkRechecksPane(t *testing.T) {
	cases := map[string]func(p *herdr.Pane){
		"now working":     func(p *herdr.Pane) { p.AgentStatus = "working" },
		"now blocked":     func(p *herdr.Pane) { p.AgentStatus = "blocked" },
		"other session":   func(p *herdr.Pane) { p.AgentSession = &herdr.AgentSession{Kind: "id", Value: "ffff0000-bbbb"} },
		"agent exited":    func(p *herdr.Pane) { p.Agent, p.AgentStatus, p.AgentSession = "", "unknown", nil },
		"different agent": func(p *herdr.Pane) { p.Agent = "codex" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			pane := agentPane()
			mutate(&pane)
			f := &fake{panes: map[string]herdr.Pane{"w3:pZ": pane}}
			p, store := newParker(t, f)
			if err := p.Park(liveRow(), ParkOptions{}); err == nil {
				t.Fatal("Park succeeded, want refusal")
			}
			if slices.Contains(f.calls, "prompt w3:pZ /exit") {
				t.Errorf("sent /exit: %v", f.calls)
			}
			if e, _ := store.Load(); len(e) != 0 {
				t.Errorf("state written: %+v", e)
			}
		})
	}
	// A pane gone entirely is refused too.
	f := &fake{panes: map[string]herdr.Pane{}}
	p, _ := newParker(t, f)
	if err := p.Park(liveRow(), ParkOptions{}); err == nil {
		t.Fatal("parked a missing pane")
	}
}

func TestParkRecordsFreedMemory(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": agentPane()}}
	p, store := newParker(t, f)
	r := liveRow()
	r.RSSBytes = 210 << 20
	if err := p.Park(r, ParkOptions{}); err != nil {
		t.Fatal(err)
	}
	rows, err := p.List()
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := store.Load(); len(e) != 1 || e[0].FreedBytes != 210<<20 {
		t.Fatalf("state = %+v", e)
	}
	if len(rows) != 1 || rows[0].FreedBytes != 210<<20 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestCodexExitAndResume(t *testing.T) {
	pane := agentPane()
	pane.Agent, pane.AgentStatus = "codex", "unknown"
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": pane}}
	p, store := newParker(t, f)
	r := liveRow()
	r.Kind, r.Status = "codex", "unknown"

	if err := p.Park(r, ParkOptions{}); err == nil {
		t.Fatal("parked a codex session in unknown state without --force")
	}
	if err := p.Park(r, ParkOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls, "prompt w3:pZ /quit") {
		t.Errorf("calls = %v, want /quit", f.calls)
	}
	entries, _ := store.Load()
	if len(entries) != 1 || entries[0].Kind != "codex" {
		t.Fatalf("entries = %+v", entries)
	}

	parked := r
	parked.Status, parked.PaneExists = sessions.StatusParked, true
	if _, err := p.Unpark(parked); err != nil {
		t.Fatal(err)
	}
	if last := f.calls[len(f.calls)-1]; !strings.HasPrefix(last, "start deploy-session codex w3:pZ resume 036f9671-aaaa") {
		t.Errorf("start call = %q", last)
	}
}

func TestOpenCodeExitAndResume(t *testing.T) {
	pane := agentPane()
	pane.Agent = "opencode"
	pane.AgentSession = &herdr.AgentSession{Kind: "id", Value: "ses_4f2a9c"}
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": pane}}
	p, _ := newParker(t, f)
	r := liveRow()
	r.Kind, r.SessionID, r.AgentRef = "opencode", "ses_4f2a9c", "ses_4f2a9c"

	if err := p.Park(r, ParkOptions{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls, "prompt w3:pZ /exit") {
		t.Errorf("calls = %v, want /exit", f.calls)
	}
	parked := r
	parked.Status, parked.PaneExists = sessions.StatusParked, true
	if _, err := p.Unpark(parked); err != nil {
		t.Fatal(err)
	}
	if last := f.calls[len(f.calls)-1]; !strings.HasPrefix(last, "start deploy-session opencode w3:pZ --session ses_4f2a9c") {
		t.Errorf("start call = %q", last)
	}
}

func TestInTab(t *testing.T) {
	row := func(name, tab, pane, status string) sessions.Row {
		return sessions.Row{Name: name, TabID: tab, PaneID: pane, Status: status, SessionID: name + "-id"}
	}
	rows := []sessions.Row{
		row("a", "w3:t1", "w3:p1", "idle"),
		row("b", "w3:t1", "w3:p2", "working"),
		row("me", "w3:t1", "w3:p3", "idle"), // the caller
		row("c", "w3:t2", "w3:p4", "idle"),  // another tab
		row("d", "w3:t1", "w3:p5", sessions.StatusParked),
		row("e", "w3:t1", "w3:p6", "done"),
	}
	ready, busy := InTab(rows, "w3:t1", "w3:p3")
	if got := names(ready); !slices.Equal(got, []string{"a", "e"}) {
		t.Errorf("ready = %v", got)
	}
	if got := names(busy); !slices.Equal(got, []string{"b"}) {
		t.Errorf("busy = %v", got)
	}
	if got := names(ParkedInTab(rows, "w3:t1")); !slices.Equal(got, []string{"d"}) {
		t.Errorf("parked in tab = %v", got)
	}
}

func names(rows []sessions.Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

// CanPark and CanUnpark back --dry-run: they agree with Park and Unpark and
// touch nothing.
func TestCanParkCanUnpark(t *testing.T) {
	working := liveRow()
	working.Status = "working"
	if CanPark(liveRow(), ParkOptions{}) != nil || CanPark(working, ParkOptions{}) == nil {
		t.Error("CanPark disagrees on idle/working")
	}
	if CanPark(working, ParkOptions{Force: true}) != nil {
		t.Error("CanPark refuses a forced park")
	}
	if CanUnpark(parkedRow(true)) != nil || CanUnpark(liveRow()) == nil {
		t.Error("CanUnpark disagrees on parked/live")
	}
	pending := parkedRow(true)
	pending.Pending = true
	if CanUnpark(pending) == nil {
		t.Error("CanUnpark accepts a pending park")
	}
}

func TestParkSavesWorkspaceLabel(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": agentPane()}}
	p, store := newParker(t, f)
	r := liveRow()
	r.Workspace = "infra"
	if err := p.Park(r, ParkOptions{}); err != nil {
		t.Fatal(err)
	}
	if e, _ := store.Load(); len(e) != 1 || e[0].Workspace != "infra" {
		t.Fatalf("entries = %+v", e)
	}
}

func TestUnparkPaneChecks(t *testing.T) {
	t.Run("List found the pane id elsewhere after a restart", func(t *testing.T) {
		f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w3", ForegroundCwd: "/repo"}},
			newTab: "w3:p50"}
		p, _ := newParker(t, f)
		_ = p.Store.Put(parkedEntry())
		pane, err := p.Unpark(parkedRow(false))
		if err != nil || pane != "w3:p50" {
			t.Fatalf("pane=%s err=%v, want a new tab", pane, err)
		}
	})
	t.Run("pane busy with another program", func(t *testing.T) {
		f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w3", ForegroundCwd: "/elsewhere"}},
			procs: map[string][]herdr.Process{"w3:pZ": {{PID: 1, Argv0: "-zsh"}, {PID: 2, Argv0: "/usr/bin/vim"}}}}
		p, _ := newParker(t, f)
		_ = p.Store.Put(parkedEntry())
		if _, err := p.Unpark(parkedRow(true)); err == nil || !strings.Contains(err.Error(), "vim") {
			t.Fatalf("err = %v, want refusal naming vim", err)
		}
		if len(f.calls) != 0 {
			t.Errorf("typed into a busy pane: %v", f.calls)
		}
	})
	t.Run("login shell is fine", func(t *testing.T) {
		f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w3", ForegroundCwd: "/repo"}},
			procs: map[string][]herdr.Process{"w3:pZ": {{PID: 1, Argv0: "-zsh"}}}}
		p, _ := newParker(t, f)
		_ = p.Store.Put(parkedEntry())
		if _, err := p.Unpark(parkedRow(true)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("pane id now in another workspace", func(t *testing.T) {
		f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w9"}}, newTab: "w3:p50"}
		p, _ := newParker(t, f)
		_ = p.Store.Put(parkedEntry())
		pane, err := p.Unpark(parkedRow(true))
		if err != nil || pane != "w3:p50" {
			t.Fatalf("pane=%s err=%v, want a new tab", pane, err)
		}
	})
	t.Run("pane runs another agent", func(t *testing.T) {
		f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w3", Agent: "claude"}}, newTab: "w3:p50"}
		p, _ := newParker(t, f)
		_ = p.Store.Put(parkedEntry())
		if pane, err := p.Unpark(parkedRow(true)); err != nil || pane != "w3:p50" {
			t.Fatalf("pane=%s err=%v, want a new tab", pane, err)
		}
	})
}

func TestUnsafeInputsRefused(t *testing.T) {
	cases := map[string]func(r *sessions.Row){
		"ctrl-c in cwd":  func(r *sessions.Row) { r.Cwd = "/repo/x\x03touch pwned #" },
		"newline in cwd": func(r *sessions.Row) { r.Cwd = "/repo/x\nid" },
		"flag as id":     func(r *sessions.Row) { r.SessionID = "--dangerously-skip-permissions" },
		"spaces in id":   func(r *sessions.Row) { r.SessionID = "abc def" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fake{panes: map[string]herdr.Pane{"w3:pZ": agentPane()}}
			p, _ := newParker(t, f)
			r := liveRow()
			mutate(&r)
			r.AgentRef = r.SessionID
			if err := p.Park(r, ParkOptions{}); err == nil {
				t.Error("Park accepted it")
			}
			r.Status, r.PaneExists = sessions.StatusParked, true
			f.panes["w3:pZ"] = herdr.Pane{PaneID: "w3:pZ", WorkspaceID: "w3"}
			if _, err := p.Unpark(r); err == nil {
				t.Error("Unpark accepted it")
			}
			if len(f.calls) != 0 {
				t.Errorf("herdr was called: %v", f.calls)
			}
		})
	}
}

func TestParkAllSkipsSessionsUsedAgain(t *testing.T) {
	pane := agentPane()
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": pane}}
	p, _ := newParker(t, f)
	stale := liveRow()
	stale.LastActive = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	// The fake herdr has no session files, so a fresh List reports a zero
	// LastActive: not newer, so the session is still parked.
	n, err := p.ParkAll([]sessions.Row{stale})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}

	// A session that is no longer listed at all is skipped, not parked.
	f2 := &fake{panes: map[string]herdr.Pane{}}
	p2, _ := newParker(t, f2)
	n, err = p2.ParkAll([]sessions.Row{stale})
	if n != 0 || err == nil {
		t.Fatalf("n=%d err=%v, want a skip", n, err)
	}
	if len(f2.calls) != 0 {
		t.Errorf("calls = %v", f2.calls)
	}
}

func TestUnparkRechecksAfterLock(t *testing.T) {
	t.Run("no longer parked", func(t *testing.T) {
		f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w3"}}}
		p, _ := newParker(t, f)
		if _, err := p.Unpark(parkedRow(true)); err == nil {
			t.Fatal("resumed a session with no parked entry")
		}
		if len(f.calls) != 0 {
			t.Errorf("calls = %v", f.calls)
		}
	})
	t.Run("already running elsewhere", func(t *testing.T) {
		f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w3"}, "w3:p9": agentPane()}}
		p9 := f.panes["w3:p9"]
		p9.PaneID = "w3:p9"
		f.panes["w3:p9"] = p9
		p, _ := newParker(t, f)
		_ = p.Store.Put(parkedEntry())
		if _, err := p.Unpark(parkedRow(true)); err == nil || !strings.Contains(err.Error(), "already running") {
			t.Fatalf("err = %v, want already running", err)
		}
		if len(f.calls) != 0 {
			t.Errorf("calls = %v", f.calls)
		}
	})
	t.Run("still exiting", func(t *testing.T) {
		f := &fake{panes: map[string]herdr.Pane{}}
		p, _ := newParker(t, f)
		_ = p.Store.Put(parkedEntry())
		r := parkedRow(true)
		r.Pending = true
		if _, err := p.Unpark(r); err == nil {
			t.Fatal("resumed a pending park")
		}
	})
	t.Run("pane contents unknown opens a tab", func(t *testing.T) {
		f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w3"}},
			procs: map[string][]herdr.Process{"w3:pZ": nil}, newTab: "w3:p50"}
		p, _ := newParker(t, f)
		_ = p.Store.Put(parkedEntry())
		if pane, err := p.Unpark(parkedRow(true)); err != nil || pane != "w3:p50" {
			t.Fatalf("pane=%s err=%v, want a new tab", pane, err)
		}
	})
}

// fish, nu and csh don't all honour POSIX single quotes, so a cd is never
// typed into them: the session resumes in a new tab opened in its folder.
func TestUnparkNonPOSIXShell(t *testing.T) {
	t.Run("cd needed", func(t *testing.T) {
		f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w3", ForegroundCwd: "/elsewhere"}},
			procs: map[string][]herdr.Process{"w3:pZ": {{PID: 1, Argv0: "-fish"}}}, newTab: "w3:p50"}
		p, _ := newParker(t, f)
		_ = p.Store.Put(parkedEntry())
		pane, err := p.Unpark(parkedRow(true))
		if err != nil || pane != "w3:p50" {
			t.Fatalf("pane=%s err=%v, want a new tab", pane, err)
		}
		for _, c := range f.calls {
			if strings.HasPrefix(c, "run ") {
				t.Errorf("typed into a fish pane: %q", c)
			}
		}
	})
	t.Run("already in the folder", func(t *testing.T) {
		f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w3", ForegroundCwd: "/repo"}},
			procs: map[string][]herdr.Process{"w3:pZ": {{PID: 1, Argv0: "fish"}}}}
		p, _ := newParker(t, f)
		_ = p.Store.Put(parkedEntry())
		if pane, err := p.Unpark(parkedRow(true)); err != nil || pane != "w3:pZ" {
			t.Fatalf("pane=%s err=%v, want the same pane", pane, err)
		}
	})
}

func TestSafeCwd(t *testing.T) {
	for dir, want := range map[string]bool{
		"/repo":      true,
		"/src/café":  true,
		"/a\nb":      false,
		"/a\x1b[31m": false,
		"/a\u009bb":  false, // C1 CSI
		"/a\xffb":    false, // invalid UTF-8
		"/a\x7fb":    false,
	} {
		if got := safeCwd(dir); got != want {
			t.Errorf("safeCwd(%q) = %v, want %v", dir, got, want)
		}
	}
}

func TestCloseLiveSession(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": agentPane()}, alive: map[int]bool{}}
	p, store := newParker(t, f)
	if err := p.Close(liveRow()); err != nil {
		t.Fatal(err)
	}
	want := []string{"prompt w3:pZ /exit", "close w3:pZ"}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
	if entries, _ := store.Load(); len(entries) != 0 {
		t.Fatalf("closing must not record a parked session: %v", entries)
	}
}

func TestCloseParkedSession(t *testing.T) {
	shell := herdr.Pane{PaneID: "w3:pZ", WorkspaceID: "w3"}
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": shell}}
	p, store := newParker(t, f)
	_ = store.Put(parkedEntry())
	if err := p.Close(parkedRow(true)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.calls, []string{"close w3:pZ"}) {
		t.Fatalf("calls %v", f.calls)
	}
	if entries, _ := store.Load(); len(entries) != 0 {
		t.Fatalf("entry kept: %v", entries)
	}
}

// parked* has no pane to close: x only takes it off the list.
func TestClosePaneAlreadyGone(t *testing.T) {
	// The id may even name an unrelated pane by now: it must be left alone.
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": {PaneID: "w3:pZ", WorkspaceID: "w9"}}}
	p, store := newParker(t, f)
	_ = store.Put(parkedEntry())
	if err := p.Close(parkedRow(false)); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("touched herdr: %v", f.calls)
	}
	if entries, _ := store.Load(); len(entries) != 0 {
		t.Fatalf("entry kept: %v", entries)
	}
}

func TestCloseRefusals(t *testing.T) {
	session := func(status string) herdr.Pane {
		p := agentPane()
		p.AgentStatus = status
		return p
	}
	row := func(status string) sessions.Row {
		r := liveRow()
		r.Status = status
		return r
	}
	shell := herdr.Pane{PaneID: "w3:pZ", WorkspaceID: "w3"}
	vim := map[string][]herdr.Process{"w3:pZ": {{PID: 5, Argv0: "vim"}}}
	pending := parkedRow(true)
	pending.Pending = true

	cases := []struct {
		name  string
		row   sessions.Row
		pane  herdr.Pane
		procs map[string][]herdr.Process
		want  string
	}{
		{name: "working", row: row("working"), pane: session("working"), want: "is working"},
		{name: "blocked", row: row("blocked"), pane: session("blocked"), want: "waiting on a question"},
		{name: "started working since listed", row: row("idle"), pane: session("working"), want: "is working"},
		{name: "other session in the pane", row: func() sessions.Row { r := row("idle"); r.AgentRef = "other"; return r }(), pane: session("idle"), want: "different session"},
		{name: "still exiting", row: pending, pane: shell, want: "still exiting"},
		{name: "parked pane runs vim", row: parkedRow(true), pane: shell, procs: vim, want: "running vim"},
		{name: "parked pane resumed elsewhere", row: parkedRow(true), pane: session("idle"), want: "now runs claude"},
		{name: "parked pane in another workspace", row: parkedRow(true), pane: herdr.Pane{PaneID: "w3:pZ", WorkspaceID: "w9"}, want: "another workspace"},
		{name: "parked pane unreadable", row: parkedRow(true), pane: shell, procs: map[string][]herdr.Process{"w3:pZ": {}}, want: "can't tell"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{panes: map[string]herdr.Pane{"w3:pZ": c.pane}, procs: c.procs}
			p, store := newParker(t, f)
			_ = store.Put(parkedEntry())
			err := p.Close(c.row)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
			if len(f.calls) != 0 {
				t.Fatalf("acted on a refused session: %v", f.calls)
			}
			if entries, _ := store.Load(); len(entries) != 1 {
				t.Fatalf("a refused close changed the list: %v", entries)
			}
		})
	}
}

func TestCloseAgentWontExitKeepsPane(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": agentPane()}, stuck: true}
	p, _ := newParker(t, f)
	err := p.Close(liveRow())
	if err == nil || !strings.Contains(err.Error(), "pane stays open") {
		t.Fatalf("err %v", err)
	}
	if slices.Contains(f.calls, "close w3:pZ") {
		t.Fatal("closed a pane whose agent is still running")
	}
}

// The agent exited, but the shell started something else: not ours to close.
func TestCloseLeavesPaneRunningAnotherProgram(t *testing.T) {
	f := &fake{panes: map[string]herdr.Pane{"w3:pZ": agentPane()}, alive: map[int]bool{},
		procs: map[string][]herdr.Process{"w3:pZ": {{PID: 5, Argv0: "vim"}}}}
	p, _ := newParker(t, f)
	if err := p.Close(liveRow()); err == nil || !strings.Contains(err.Error(), "running vim") {
		t.Fatalf("err %v", err)
	}
	if slices.Contains(f.calls, "close w3:pZ") {
		t.Fatal("closed a busy pane")
	}
}
