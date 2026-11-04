package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
