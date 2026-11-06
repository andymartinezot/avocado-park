package sessions

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/andymartinezot/avocado-park/internal/herdr"
	"github.com/andymartinezot/avocado-park/internal/state"
)

type fakeHerdr struct {
	agents, panes []herdr.Pane
	tabs          []herdr.Tab
	procs         map[string][]herdr.Process
}

func (f fakeHerdr) Agents() ([]herdr.Pane, error) { return f.agents, nil }
func (f fakeHerdr) Panes() ([]herdr.Pane, error)  { return f.panes, nil }
func (f fakeHerdr) Workspaces() ([]herdr.Workspace, error) {
	return []herdr.Workspace{{WorkspaceID: "w3", Label: "🌟 - Platform"}}, nil
}
func (f fakeHerdr) Tabs() ([]herdr.Tab, error)                            { return f.tabs, nil }
func (f fakeHerdr) ForegroundProcesses(p string) ([]herdr.Process, error) { return f.procs[p], nil }

func writeSession(t *testing.T, dir string, body string, pid int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuild(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	claude := t.TempDir()
	// Session file overrides herdr's auto title with the /rename name.
	writeSession(t, claude, `{"pid":101,"sessionId":"aaaaaaaa-1","name":"billing-83","updatedAt":`+
		strconv.FormatInt(now.Add(-8*24*time.Hour).UnixMilli(), 10)+`}`, 101)

	h := fakeHerdr{
		agents: []herdr.Pane{
			{PaneID: "w3:pN", WorkspaceID: "w3", Agent: "claude", AgentStatus: "idle",
				TerminalTitleStripped: "PROJ-1234 resume handoff", Cwd: "/repo",
				AgentSession: &herdr.AgentSession{Kind: "id", Value: "aaaaaaaa-1"}},
			{PaneID: "w3:p2", WorkspaceID: "w3", Agent: "claude", AgentStatus: "working",
				TerminalTitleStripped: "busy", Cwd: "/repo",
				AgentSession: &herdr.AgentSession{Kind: "id", Value: "bbbbbbbb-2"}},
			{PaneID: "w3:p3", WorkspaceID: "w3", Agent: "codex", AgentStatus: "unknown", Cwd: "/srv",
				AgentSession: &herdr.AgentSession{Kind: "id", Value: "eeeeeeee-5"}},
			{PaneID: "w3:p4", Agent: "pi", AgentStatus: "idle"}, // not managed
		},
		panes: []herdr.Pane{{PaneID: "w3:pN", WorkspaceID: "w3"}, {PaneID: "w3:p2", WorkspaceID: "w3"},
			{PaneID: "w3:p3", WorkspaceID: "w3"}, {PaneID: "w3:p4", WorkspaceID: "w3"}, {PaneID: "w3:p9", WorkspaceID: "w3"}},
		procs: map[string][]herdr.Process{
			"w3:pN": {{PID: 101, Argv0: "claude"}},
			"w3:p2": {{PID: 102, Argv0: "/usr/local/bin/claude"}},
			"w3:p3": {{PID: 103, Argv0: "codex"}},
		},
	}
	parked := []state.Entry{
		{SessionID: "cccccccc-3", Name: "old", PaneID: "w3:p9", WorkspaceID: "w3", ParkedAt: now.Add(-time.Hour)},
		{SessionID: "dddddddd-4", Name: "closed", PaneID: "w3:p77", WorkspaceID: "w3", ParkedAt: now.Add(-2 * time.Hour)},
		{SessionID: "bbbbbbbb-2", Name: "busy", PaneID: "w3:p2"}, // resumed by hand
	}
	res, err := Build(h, Inputs{ClaudeDir: claude, Parked: parked, Now: now,
		RSS: func(pid int) int64 { return int64(pid) << 20 }})
	if err != nil {
		t.Fatal(err)
	}

	if got := len(res.Rows); got != 5 {
		t.Fatalf("rows = %d, want 5 (2 live claude + 1 codex + 2 parked): %+v", got, res.Rows)
	}
	if len(res.Resumed) != 1 || res.Resumed[0] != "bbbbbbbb-2" {
		t.Errorf("Resumed = %v", res.Resumed)
	}
	// Live sorted by activity: "busy" and the codex session have no file (zero
	// time) so they sort after billing-83, then by name.
	want := []string{"billing-83", "busy", "srv", "old", "closed"}
	for i, w := range want {
		if res.Rows[i].Name != w {
			t.Errorf("row %d = %q, want %q", i, res.Rows[i].Name, w)
		}
	}
	r := res.Rows[0]
	if r.PID != 101 || r.RSSBytes != 101<<20 || r.Workspace != "🌟 - Platform" {
		t.Errorf("enrichment wrong: %+v", r)
	}
	if got := r.IdleFor(now); got != 8*24*time.Hour {
		t.Errorf("IdleFor = %v", got)
	}
	if c := res.Rows[2]; c.Kind != "codex" || c.PID != 103 || c.SessionID != "eeeeeeee-5" {
		t.Errorf("codex row wrong: %+v", c)
	}
	if res.Rows[3].Kind != "claude" {
		t.Errorf("parked entry without a kind should default to claude: %+v", res.Rows[3])
	}
	if !res.Rows[3].PaneExists || res.Rows[4].PaneExists {
		t.Errorf("PaneExists: old=%v closed=%v", res.Rows[3].PaneExists, res.Rows[4].PaneExists)
	}
}

// After a herdr restart ids are reassigned: a parked session finds its
// workspace by label, and a pane id now in another workspace isn't its pane.
func TestBuildAfterRestart(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	h := fakeHerdr{panes: []herdr.Pane{{PaneID: "w3:p9", WorkspaceID: "w3"}}}
	parked := []state.Entry{
		{SessionID: "aaaaaaaa-1", Name: "moved", PaneID: "w7:p1", WorkspaceID: "w7", Workspace: "🌟 - Platform"},
		{SessionID: "bbbbbbbb-2", Name: "gone", PaneID: "w3:p9", WorkspaceID: "w3", Workspace: "deleted"},
		{SessionID: "cccccccc-3", Name: "same", PaneID: "w3:p9", WorkspaceID: "w3", Workspace: "🌟 - Platform"},
	}
	res, err := Build(h, Inputs{Parked: parked, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Row{}
	for _, r := range res.Rows {
		got[r.Name] = r
	}
	if r := got["moved"]; r.WorkspaceID != "w3" || r.Workspace != "🌟 - Platform" || r.PaneExists {
		t.Errorf("moved: %+v", r)
	}
	if r := got["gone"]; r.WorkspaceID != "" || r.Workspace != "deleted" || r.PaneExists {
		t.Errorf("gone: %+v", r)
	}
	if r := got["same"]; r.WorkspaceID != "w3" || !r.PaneExists {
		t.Errorf("same: %+v", r)
	}
}

func TestBuildTabAndPaneNames(t *testing.T) {
	// As in herdr: agent.list carries no label, only pane.list does.
	agent := herdr.Pane{PaneID: "w3:p1", TabID: "w3:t1", WorkspaceID: "w3", Agent: "claude", AgentStatus: "idle",
		TerminalTitleStripped: "live", AgentSession: &herdr.AgentSession{Kind: "id", Value: "live-1"}}
	labelled := agent
	labelled.Label = "api pane"
	h := fakeHerdr{
		agents: []herdr.Pane{agent},
		panes: []herdr.Pane{labelled,
			{PaneID: "w3:p2", TabID: "w3:t2", WorkspaceID: "w3", Label: "renamed"},
			{PaneID: "w3:p3", TabID: "w3:t2", WorkspaceID: "w3"},
			{PaneID: "w3:p4", TabID: "w3:t2", WorkspaceID: "w3", Label: "other", Agent: "codex"},
		},
		tabs: []herdr.Tab{{TabID: "w3:t1", Label: "Backend"}, {TabID: "w3:t2", Label: "Docs now"}},
	}
	parked := func(id, pane string) state.Entry {
		return state.Entry{SessionID: id, Name: id, PaneID: pane, TabID: "w3:t9", WorkspaceID: "w3",
			Tab: "Docs then", PaneLabel: "saved"}
	}
	old := parked("old", "w3:p3") // written before names were saved
	old.Tab, old.PaneLabel = "", ""
	res, err := Build(h, Inputs{Now: time.Now(), Parked: []state.Entry{
		parked("here", "w3:p2"), parked("unnamed", "w3:p3"), parked("gone", "w3:p8"), parked("taken", "w3:p4"), old,
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"live":    {"Backend", "api pane"},
		"here":    {"Docs now", "renamed"}, // current names win while the pane is the session's
		"unnamed": {"Docs now", ""},        // the pane's name was removed
		"gone":    {"Docs then", "saved"},  // pane closed: the saved names
		"taken":   {"Docs then", "saved"},  // the pane runs another agent now
		"old":     {"Docs now", ""},
	}
	if len(res.Rows) != len(want) {
		t.Fatalf("rows %+v", res.Rows)
	}
	for _, r := range res.Rows {
		if w := want[r.Name]; r.Tab != w[0] || r.PaneLabel != w[1] {
			t.Errorf("%s: tab %q pane %q, want %q", r.Name, r.Tab, r.PaneLabel, w)
		}
	}
}

func TestRSSTree(t *testing.T) {
	ps := []byte(`    1     0   100
   10     1  1000
   11    10   200
   12    11    50
   20     1   999
`)
	rss := rssFromPS(ps)
	if got := rss(10); got != 1250*1024 {
		t.Errorf("rss(10) = %d, want %d", got, 1250*1024)
	}
	if got := rss(42); got != 0 {
		t.Errorf("rss(missing) = %d", got)
	}
}

func TestFormat(t *testing.T) {
	cases := map[time.Duration]string{30 * time.Second: "now", 12 * time.Minute: "12m", 30 * time.Hour: "30h", 9 * 24 * time.Hour: "9d"}
	for d, want := range cases {
		if got := FormatAge(d); got != want {
			t.Errorf("FormatAge(%v) = %q, want %q", d, got, want)
		}
	}
	if got := FormatBytes(291 << 20); got != "291M" {
		t.Errorf("FormatBytes = %q", got)
	}
}

func TestBuildPendingPark(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	live := herdr.Pane{PaneID: "w3:p1", WorkspaceID: "w3", Agent: "claude", AgentStatus: "idle",
		AgentSession: &herdr.AgentSession{Kind: "id", Value: "aaaaaaaa-1"}}
	h := fakeHerdr{agents: []herdr.Pane{live}, panes: []herdr.Pane{live, {PaneID: "w3:p2", WorkspaceID: "w3"}}}
	build := func(parked ...state.Entry) Result {
		t.Helper()
		res, err := Build(h, Inputs{Parked: parked, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// Still exiting: the entry is kept, and only the live row is shown.
	res := build(state.Entry{SessionID: "aaaaaaaa-1", PaneID: "w3:p1", Pending: true, ParkedAt: now.Add(-time.Minute)})
	if len(res.Resumed) != 0 || len(res.Rows) != 1 || res.Rows[0].Parked() {
		t.Errorf("recent pending park: resumed=%v rows=%+v", res.Resumed, res.Rows)
	}
	// Still live long after: the park was abandoned, the session is running.
	res = build(state.Entry{SessionID: "aaaaaaaa-1", PaneID: "w3:p1", Pending: true, ParkedAt: now.Add(-time.Hour)})
	if len(res.Resumed) != 1 {
		t.Errorf("old pending park not dropped: %v", res.Resumed)
	}
	// A plain parked entry whose session is live again was resumed by hand.
	res = build(state.Entry{SessionID: "aaaaaaaa-1", PaneID: "w3:p1", ParkedAt: now.Add(-time.Minute)})
	if len(res.Resumed) != 1 {
		t.Errorf("resumed-by-hand entry kept: %v", res.Resumed)
	}
	// The agent exited after the wait gave up: the park is complete.
	res = build(state.Entry{SessionID: "bbbbbbbb-2", PaneID: "w3:p2", Pending: true, ParkedAt: now.Add(-time.Hour)})
	if r := res.Rows[len(res.Rows)-1]; !r.Parked() || r.Pending || !r.PaneExists {
		t.Errorf("exited pending park: %+v", r)
	}
}

func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"deploy-session": "deploy-session",
		"café ✳":         "café ✳",
		"a\x1b]0;x\x07b": "a?]0;x?b",
		"a\u009b31mb":    "a?31mb",
		"a\xffb":         "a?b",
	} {
		if got := Printable(in); got != want {
			t.Errorf("Printable(%q) = %q, want %q", in, got, want)
		}
	}
}
