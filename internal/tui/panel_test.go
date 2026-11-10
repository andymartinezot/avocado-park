package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/andymartinezot/avocado-park/internal/herdr"
	"github.com/andymartinezot/avocado-park/internal/park"
	"github.com/andymartinezot/avocado-park/internal/sessions"
	"github.com/andymartinezot/avocado-park/internal/state"
)

// fakeHerdr is a herdr with panes kept in memory; /exit turns an agent pane
// into a plain shell.
type fakeHerdr struct {
	panes  map[string]herdr.Pane
	closed []string
}

func (f *fakeHerdr) Agents() ([]herdr.Pane, error) {
	var out []herdr.Pane
	for _, p := range f.panes {
		if p.Agent != "" {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakeHerdr) Panes() ([]herdr.Pane, error) {
	var out []herdr.Pane
	for _, p := range f.panes {
		out = append(out, p)
	}
	return out, nil
}
func (f *fakeHerdr) Workspaces() ([]herdr.Workspace, error) { return nil, nil }
func (f *fakeHerdr) Tabs() ([]herdr.Tab, error)             { return nil, nil }
func (f *fakeHerdr) ForegroundProcesses(string) ([]herdr.Process, error) {
	return []herdr.Process{{PID: 1, Argv0: "-zsh"}}, nil
}
func (f *fakeHerdr) Pane(id string) (herdr.Pane, error) {
	p, ok := f.panes[id]
	if !ok {
		return p, &herdr.Error{Code: "pane_not_found"}
	}
	return p, nil
}
func (f *fakeHerdr) Prompt(target, _ string) error {
	p := f.panes[target]
	p.Agent, p.AgentStatus, p.AgentSession = "", "", nil
	f.panes[target] = p
	return nil
}
func (f *fakeHerdr) RunInPane(string, string) error                                   { return nil }
func (f *fakeHerdr) StartAgent(string, string, string, []string, time.Duration) error { return nil }
func (f *fakeHerdr) CreateTab(string, string, string) (string, error)                 { return "", nil }
func (f *fakeHerdr) FocusPane(string) error                                           { return nil }
func (f *fakeHerdr) ClosePane(id string) error {
	f.closed = append(f.closed, id)
	delete(f.panes, id)
	return nil
}

func agentPane(id, session string) herdr.Pane {
	return herdr.Pane{PaneID: id, Agent: "claude", AgentStatus: "idle", TerminalTitleStripped: session,
		AgentSession: &herdr.AgentSession{Kind: "id", Value: session}}
}

func newModel(t *testing.T, f *fakeHerdr) model {
	t.Helper()
	store := state.Store{Path: filepath.Join(t.TempDir(), "parked.json")}
	p := &park.Parker{H: f, Store: store, ClaudeDir: t.TempDir(), Alive: func(int) bool { return false },
		Now: time.Now, Sleep: func(time.Duration) {}, ExitTimeout: time.Second}
	return model{p: p, opt: Options{IdleDays: 3, Refresh: time.Millisecond}, w: 100, h: 24, loaded: true}
}

// commands runs cmd, unpacking a batch, and returns the messages it produced.
func commands(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		out = append(out, commands(c)...)
	}
	return out
}

// A batch can run for many seconds. The list must keep following it, so each
// session shows as parked while the rest are still exiting.
func TestRefreshesDuringAnAction(t *testing.T) {
	m := newModel(t, &fakeHerdr{panes: map[string]herdr.Pane{"p1": agentPane("p1", "one")}})
	m.busy = "parking 5 sessions…"
	mm, cmd := m.Update(tickMsg{})
	m = mm.(model)
	var loaded bool
	for _, msg := range commands(cmd) {
		if r, ok := msg.(rowsMsg); ok && len(r.rows) == 1 {
			loaded = true
		}
	}
	if !loaded {
		t.Fatal("a tick during an action did not reload the list")
	}
}

// parked* only makes sense with its explanation on screen.
func TestLegendExplainsTheStar(t *testing.T) {
	m := newModel(t, &fakeHerdr{panes: map[string]herdr.Pane{}})
	m.setRows([]sessions.Row{{Name: "old", SessionID: "a", Status: sessions.StatusParked, PaneExists: false}})
	view := m.View()
	if !strings.Contains(view, "parked*") || !strings.Contains(view, "* pane gone") {
		t.Fatalf("no legend for parked*:\n%s", view)
	}
	m.setRows([]sessions.Row{{Name: "old", SessionID: "a", Status: sessions.StatusParked, PaneExists: true}})
	if strings.Contains(m.View(), "* pane gone") {
		t.Fatal("legend shown with no parked* row")
	}
}

func TestRefreshIsFasterDuringAnAction(t *testing.T) {
	m := model{opt: Options{Refresh: 3 * time.Second}}
	if got := m.tickEvery(); got != 3*time.Second {
		t.Fatalf("idle refresh %s", got)
	}
	m.busy = "parking…"
	if got := m.tickEvery(); got != time.Second {
		t.Fatalf("refresh during an action %s", got)
	}
	m.opt.Refresh = 500 * time.Millisecond
	if got := m.tickEvery(); got != 500*time.Millisecond {
		t.Fatalf("a faster configured refresh must stay: %s", got)
	}
}

func press(t *testing.T, m model, key string) (model, tea.Cmd) {
	t.Helper()
	mm, cmd := m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return mm.(model), cmd
}

func pressKey(m model, k tea.KeyType) (model, tea.Cmd) {
	mm, cmd := m.key(tea.KeyMsg{Type: k})
	return mm.(model), cmd
}

func TestCloseAsksFirst(t *testing.T) {
	f := &fakeHerdr{panes: map[string]herdr.Pane{"p1": agentPane("p1", "one")}}
	m := newModel(t, f)
	rows, err := m.p.List()
	if err != nil {
		t.Fatal(err)
	}
	m.setRows(rows)

	m, cmd := press(t, m, "x")
	if cmd != nil || len(f.closed) != 0 {
		t.Fatal("x acted without asking")
	}
	if !strings.Contains(m.View(), "close its pane") {
		t.Fatalf("no question on screen:\n%s", m.View())
	}
	m, _ = press(t, m, "n")
	if m.confirm != nil || len(f.closed) != 0 {
		t.Fatal("n did not cancel")
	}

	m, _ = press(t, m, "x")
	m, cmd = press(t, m, "y")
	if m.busy == "" {
		t.Fatal("not busy while closing")
	}
	mm, cmd := m.Update(cmd())
	m = mm.(model)
	if m.busy != "" || m.msgErr || !strings.Contains(m.msg, "closed one") {
		t.Fatalf("busy %q msg %q", m.busy, m.msg)
	}
	if len(f.closed) != 1 || f.closed[0] != "p1" {
		t.Fatalf("closed %v", f.closed)
	}
	// The list reloads when the action ends.
	mm, _ = m.Update(cmd())
	if m = mm.(model); len(m.rows) != 0 {
		t.Fatalf("closed session still listed: %v", m.rows)
	}
}

func TestCloseRefusesOwnPaneAndBusySessions(t *testing.T) {
	f := &fakeHerdr{panes: map[string]herdr.Pane{"p1": agentPane("p1", "one")}}
	m := newModel(t, f)
	m.opt.Self = "p1"
	rows, _ := m.p.List()
	m.setRows(rows)
	m, _ = press(t, m, "x")
	if m.confirm != nil || !m.msgErr {
		t.Fatalf("own pane: confirm %v msg %q", m.confirm, m.msg)
	}

	m.opt.Self = ""
	working := m.all[0]
	working.Status = sessions.StatusWorking
	m.setRows([]sessions.Row{working})
	m, _ = press(t, m, "x")
	if m.confirm != nil || !strings.Contains(m.msg, "is working") {
		t.Fatalf("working: confirm %v msg %q", m.confirm, m.msg)
	}
}

func placed(name, ws, tab, pane string) sessions.Row {
	return sessions.Row{Name: name, SessionID: "id-" + name, PaneID: "p-" + name, Status: sessions.StatusIdle,
		Workspace: ws, Tab: tab, PaneLabel: pane, PaneExists: true}
}

func TestTabAndPaneColumns(t *testing.T) {
	m := newModel(t, &fakeHerdr{})
	m.loaded, m.w, m.h = true, 120, 20
	m.setRows([]sessions.Row{
		placed("one", "backend", "Infra tab", "api pane"),
		placed("two", "docs", "Docs tab", ""),
	})
	v := m.View()
	for _, want := range []string{"WORKSPACE", "TAB", "PANE", "Infra tab", "api pane", "Docs tab"} {
		if !strings.Contains(v, want) {
			t.Errorf("no %q in:\n%s", want, v)
		}
	}
	// A pane without a name shows a dash in its column, not a blank.
	for line := range strings.Lines(v) {
		if strings.Contains(line, "Docs tab") && !strings.Contains(line, "-") {
			t.Errorf("no dash in %q", line)
		}
	}
	if m, _ = press(t, m, "/"); len(m.rows) != 2 {
		t.Fatal("filter dropped rows before typing")
	}
	for _, r := range "api" {
		m, _ = press(t, m, string(r))
	}
	if len(m.rows) != 1 || m.rows[0].Name != "one" {
		t.Fatalf("filter by pane name: %v", m.rows)
	}
}

// The frame must never outgrow the window while it has room for the columns
// it needs, however long the labels are, and it drops pane before tab.
func TestColumnsFitTheWindow(t *testing.T) {
	long := strings.Repeat("x", 90)
	m := newModel(t, &fakeHerdr{})
	m.loaded, m.h = true, 20
	m.setRows([]sessions.Row{placed(long, long, long, long), placed("s", "w", "t", "p")})
	for w := 57; w <= 220; w++ {
		m.w = w
		v := m.View()
		for line := range strings.Lines(v) {
			if got := lipgloss.Width(strings.TrimRight(line, "\n")); got != w {
				t.Fatalf("width %d: line is %d wide:\n%s", w, got, v)
			}
		}
		tab, pane := strings.Contains(v, " TAB "), strings.Contains(v, " PANE ")
		switch {
		case w >= 77 && !(tab && pane):
			t.Fatalf("width %d: tab %v pane %v", w, tab, pane)
		case w < 77 && pane, w < 67 && tab, w >= 67 && !tab:
			t.Fatalf("width %d: tab %v pane %v", w, tab, pane)
		}
	}
}

func TestWorkspaceFilter(t *testing.T) {
	m := newModel(t, &fakeHerdr{})
	m.loaded, m.w, m.h = true, 120, 20
	all := []sessions.Row{
		placed("api", "🌟 - Backend", "t", "p"),
		placed("notes", "docs", "t", "p"),
		placed("site", "docs", "t", "p"),
	}
	m.setRows(all)
	shown := func() (names []string) {
		for _, r := range m.rows {
			names = append(names, r.Name)
		}
		return names
	}

	// w goes through the workspaces by name, ignoring the emoji, then back to all.
	for _, want := range []string{"api", "notes site", "api notes site", "api"} {
		m, _ = press(t, m, "w")
		if got := strings.Join(shown(), " "); got != want {
			t.Fatalf("after w: %q, want %q", got, want)
		}
	}
	if v := m.View(); !strings.Contains(v, "w Backend") {
		t.Errorf("workspace not in the title:\n%s", v)
	}
	m, _ = press(t, m, "W")
	if m.ws != "" {
		t.Fatalf("W from the first workspace: %q, want all", m.ws)
	}
	m, _ = press(t, m, "W")
	if m.ws != "docs" {
		t.Fatalf("W from all: %q, want the last workspace", m.ws)
	}

	// With /, both apply; esc clears the text first, then the workspace.
	m, _ = press(t, m, "/")
	m, _ = press(t, m, "s")
	m, _ = press(t, m, "i")
	m, _ = pressKey(m, tea.KeyEnter)
	if got := strings.Join(shown(), " "); got != "site" {
		t.Fatalf("workspace and text: %q", got)
	}
	m, cmd := pressKey(m, tea.KeyEsc)
	if cmd != nil || m.filter != "" || m.ws != "docs" {
		t.Fatalf("first esc: filter %q ws %q", m.filter, m.ws)
	}
	m, cmd = pressKey(m, tea.KeyEsc)
	if cmd != nil || m.ws != "" || len(m.rows) != 3 {
		t.Fatalf("second esc: ws %q rows %d", m.ws, len(m.rows))
	}

	// P acts on what is shown, so only on the chosen workspace.
	old := m.p.Now().Add(-10 * 24 * time.Hour)
	for i := range all {
		all[i].LastActive = old
	}
	m.setRows(all)
	m, _ = press(t, m, "w") // Backend
	m, _ = press(t, m, "P")
	if len(m.confirm) != 1 || m.confirm[0].Name != "api" {
		t.Fatalf("P in one workspace would park %v", m.confirm)
	}
}
