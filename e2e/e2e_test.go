//go:build e2e

// Package e2e runs avocado-park against a real herdr: a private server with its
// own home directory and socket, so the herdr you work in is never touched. The
// agent is a stand-in (fakeagent) built under the name claude.
//
//	go test -tags e2e -count=1 ./e2e
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const pluginID = "andymartinezot.avocado-park"

type env struct {
	t            *testing.T
	dir, home    string
	socket, park string // park is the avocado-park binary
	herdrBin     string
	vars         []string
}

func (e *env) cmd(bin string, args ...string) *exec.Cmd {
	c := exec.Command(bin, args...)
	c.Env, c.Dir = e.vars, e.home
	return c
}

// run returns a command's output and fails the test if the command fails.
func (e *env) run(bin string, args ...string) string {
	e.t.Helper()
	out, err := e.cmd(bin, args...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("%s %s: %v\n%s", filepath.Base(bin), strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (e *env) herdr(args ...string) string {
	e.t.Helper()
	return e.run(e.herdrBin, args...)
}

func (e *env) avocado(args ...string) string {
	e.t.Helper()
	return e.run(e.park, args...)
}

// waitFor polls until ok holds. what names the condition in the failure.
func (e *env) waitFor(what string, ok func() bool) {
	e.t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if ok() {
			return
		}
	}
	e.t.Fatalf("timed out waiting for %s", what)
}

type row struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	SessionID  string `json:"session_id"`
	PaneID     string `json:"pane_id"`
	Workspace  string `json:"workspace"`
	Tab        string `json:"tab"`
	PaneLabel  string `json:"pane_label"`
	PaneExists bool   `json:"pane_exists"`
}

func (e *env) rows() []row {
	e.t.Helper()
	var rows []row
	if err := json.Unmarshal([]byte(e.avocado("list", "--json")), &rows); err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func (e *env) session(id string) (row, bool) {
	e.t.Helper()
	for _, r := range e.rows() {
		if r.SessionID == id {
			return r, true
		}
	}
	return row{}, false
}

func (e *env) paneIDs() []string {
	e.t.Helper()
	var r struct {
		Result struct {
			Panes []struct {
				PaneID string `json:"pane_id"`
			} `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(e.herdr("pane", "list")), &r); err != nil {
		e.t.Fatal(err)
	}
	var ids []string
	for _, p := range r.Result.Panes {
		ids = append(ids, p.PaneID)
	}
	return ids
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// start builds the binaries and starts the private herdr.
func start(t *testing.T) *env {
	t.Helper()
	herdrBin, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("herdr is not installed")
	}
	// Under /tmp: a unix socket path has to stay short.
	dir, err := os.MkdirTemp("/tmp", "avocado-e2e")
	if err != nil {
		t.Fatal(err)
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	home, bin := filepath.Join(dir, "home"), filepath.Join(dir, "bin")
	e := &env{t: t, dir: dir, home: home, herdrBin: herdrBin, socket: filepath.Join(dir, "h.sock"),
		park: filepath.Join(bin, "avocado-park")}
	for _, b := range [][2]string{{"avocado-park", "../cmd/avocado-park"}, {"claude", "./fakeagent"}} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, b[0]), b[1]).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", b[0], err, out)
		}
	}
	// Built from nothing, not from os.Environ: inside herdr the inherited
	// HERDR_* variables point at the real server.
	e.vars = []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"PATH=" + strings.Join([]string{bin, filepath.Dir(herdrBin), "/usr/bin", "/bin"}, ":"),
		"TERM=xterm-256color",
		"SHELL=/bin/zsh",
		"HERDR_SOCKET_PATH=" + e.socket,
		"CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"),
		"FAKE_AGENT_LOG=" + filepath.Join(dir, "agent.log"),
	}
	write(t, filepath.Join(home, ".zshrc"), "PROMPT='$ '\n")
	if err := os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) serve() {
	t := e.t
	server := e.cmd(e.herdrBin, "server")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	e.waitFor("the private herdr", func() bool {
		_, err := os.Stat(e.socket)
		return err == nil
	})
	// Nothing below runs unless this is really the private server.
	if status := e.herdr("status"); !strings.Contains(status, "socket: "+e.socket) {
		t.Fatalf("not talking to the private herdr:\n%s", status)
	}
}

func TestLifecycle(t *testing.T) {
	e := start(t)

	// What an existing install has: settings and sessions parked by an older
	// release. All of it must survive everything below.
	herdrConfig := filepath.Join(e.home, ".config", "herdr", "config.toml")
	pluginConfig := filepath.Join(e.home, ".config", "herdr", "plugins", "config", pluginID, "config.toml")
	parkedFile := filepath.Join(e.home, ".local", "state", "avocado-park", "parked.json")
	write(t, herdrConfig, "# e2e: existing herdr config\n[ui]\nsidebar_width = 30\n")
	write(t, pluginConfig, "# e2e: existing plugin config\npark_idle_days = 7\nrefresh_seconds = 1\n")
	write(t, parkedFile, read(t, filepath.Join("..", "testdata", "parked-v0.1.0.json")))
	configs := map[string]string{herdrConfig: read(t, herdrConfig), pluginConfig: read(t, pluginConfig)}

	e.serve()

	// The manifest loads, with its actions.
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	e.herdr("plugin", "link", repo)
	if plugins := e.herdr("plugin", "list"); !strings.Contains(plugins, pluginID) {
		t.Fatalf("plugin not listed:\n%s", plugins)
	}
	actions := e.herdr("plugin", "action", "list")
	for _, a := range []string{"open-panel", "park", "unpark", "park-tab", "unpark-tab", "park-idle", "unpark-all"} {
		if !strings.Contains(actions, a) {
			t.Errorf("no action %q in:\n%s", a, actions)
		}
	}

	// A named tab and pane running an agent, with a plain pane beside it.
	var ws struct {
		Result struct {
			RootPane struct {
				PaneID string `json:"pane_id"`
				TabID  string `json:"tab_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(e.herdr("workspace", "create", "--cwd", e.home, "--label", "e2e-ws")), &ws); err != nil {
		t.Fatal(err)
	}
	pane, tab := ws.Result.RootPane.PaneID, ws.Result.RootPane.TabID
	e.herdr("tab", "rename", tab, "e2e-tab")
	e.herdr("pane", "rename", pane, "e2e-pane")
	before := e.paneIDs()
	e.herdr("pane", "split", pane, "--direction", "right", "--no-focus")
	var side string
	for _, id := range e.paneIDs() {
		if !slices.Contains(before, id) {
			side = id
		}
	}
	if side == "" {
		t.Fatal("no new pane from the split")
	}

	const id = "e2e-session-0001"
	e.herdr("pane", "run", pane, "claude")
	e.waitFor("the agent to be idle", func() bool {
		r, ok := e.session(id)
		return ok && r.Status == "idle"
	})

	// Columns: from herdr's real responses, in the list and on the panel.
	r, _ := e.session(id)
	if r.Workspace != "e2e-ws" || r.Tab != "e2e-tab" || r.PaneLabel != "e2e-pane" {
		t.Fatalf("live row: %+v", r)
	}
	frame := e.avocado("panel", "--print", "--width", "120")
	for _, want := range []string{"TAB", "PANE", "e2e-tab", "e2e-pane", "parked*"} {
		if !strings.Contains(frame, want) {
			t.Errorf("no %q on the panel:\n%s", want, frame)
		}
	}

	// Park: the agent exits, the pane stays, the names are remembered.
	e.avocado("park", id)
	e.waitFor("the session to be parked", func() bool {
		r, ok := e.session(id)
		return ok && r.Status == "parked" && r.PaneExists
	})
	if !slices.Contains(e.paneIDs(), pane) {
		t.Fatal("park closed the pane")
	}
	if s := read(t, parkedFile); !strings.Contains(s, `"tab": "e2e-tab"`) || !strings.Contains(s, `"pane_label": "e2e-pane"`) {
		t.Fatalf("names not saved:\n%s", s)
	}

	// Unpark: the same session comes back in the same pane.
	e.avocado("unpark", id)
	e.waitFor("the session to be back", func() bool {
		r, ok := e.session(id)
		return ok && r.Status == "idle" && r.PaneID == pane
	})
	if log := read(t, filepath.Join(e.dir, "agent.log")); !strings.Contains(log, "--resume "+id) {
		t.Fatalf("not resumed:\n%s", log)
	}

	// Close from the real panel, with real keys: x asks, n cancels, y closes.
	// A dry run first must change nothing.
	if out := e.avocado("close", "--dry-run", id); !strings.Contains(out, "would close") {
		t.Fatalf("dry run: %s", out)
	}
	e.herdr("pane", "run", side, e.park+" panel")
	screen := func() string { return e.herdr("pane", "read", side) }
	e.waitFor("the panel", func() bool { return strings.Contains(screen(), "e2e-agent") })
	e.herdr("pane", "send-keys", side, "x")
	e.waitFor("the question", func() bool { return strings.Contains(screen(), "y/n") })
	e.herdr("pane", "send-keys", side, "n")
	e.waitFor("the cancel", func() bool { return strings.Contains(screen(), "cancelled") })
	if !slices.Contains(e.paneIDs(), pane) {
		t.Fatal("n closed the pane")
	}
	e.herdr("pane", "send-keys", side, "x")
	e.waitFor("the question", func() bool { return strings.Contains(screen(), "y/n") })
	e.herdr("pane", "send-keys", side, "y")
	e.waitFor("the pane to close", func() bool { return !slices.Contains(e.paneIDs(), pane) })
	e.waitFor("the session to leave the list", func() bool {
		_, ok := e.session(id)
		return !ok
	})
	e.herdr("pane", "send-keys", side, "q")

	// Nothing else went with it.
	if !slices.Contains(e.paneIDs(), side) {
		t.Error("the other pane was closed too")
	}
	for path, want := range configs {
		if got := read(t, path); got != want {
			t.Errorf("%s changed:\n%s", path, got)
		}
	}
	for _, name := range []string{"eks-cluster", "no-kind"} {
		if s := read(t, parkedFile); !strings.Contains(s, fmt.Sprintf("%q", name)) {
			t.Errorf("%s is gone from the parked list:\n%s", name, s)
		}
	}
}
