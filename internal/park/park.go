// Package park parks Claude Code and Codex sessions (exits the agent but keeps its pane)
// and unparks them (resumes the same conversation in that pane).
package park

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/andymartinezot/avocado-park/internal/herdr"
	"github.com/andymartinezot/avocado-park/internal/sessions"
	"github.com/andymartinezot/avocado-park/internal/state"
)

// Herdr is the part of the herdr API that parking needs.
type Herdr interface {
	sessions.Herdr
	Pane(id string) (herdr.Pane, error)
	Prompt(target, text string) error
	RunInPane(pane, line string) error
	StartAgent(name, kind, pane string, args []string, timeout time.Duration) error
	CreateTab(workspace, cwd, label string) (string, error)
	FocusPane(pane string) error
	ClosePane(pane string) error
}

// Parker parks and unparks sessions.
type Parker struct {
	H         Herdr
	Store     state.Store
	ClaudeDir string
	// RSS is called once per List to snapshot memory use; nil skips it.
	RSS func() func(pid int) int64
	// Alive reports whether a pid still exists; defaults to a signal-0 probe.
	Alive func(pid int) bool
	Now   func() time.Time
	Sleep func(time.Duration)
	// ExitTimeout bounds the wait for an agent to exit after /exit.
	ExitTimeout time.Duration
	// StartTimeout bounds the wait for a resumed agent to become ready.
	StartTimeout time.Duration
}

// New returns a Parker with production defaults.
func New(h Herdr, store state.Store, claudeDir string) *Parker {
	return &Parker{
		H: h, Store: store, ClaudeDir: claudeDir,
		RSS:          sessions.ProcessRSS,
		Alive:        pidAlive,
		Now:          time.Now,
		Sleep:        time.Sleep,
		ExitTimeout:  20 * time.Second,
		StartTimeout: 60 * time.Second,
	}
}

// List returns every session and drops parked entries that are live again.
func (p *Parker) List() ([]sessions.Row, error) {
	parked, err := p.Store.Load()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p.Store.Path, err)
	}
	in := sessions.Inputs{ClaudeDir: p.ClaudeDir, Parked: parked, Now: p.Now()}
	if p.RSS != nil {
		in.RSS = p.RSS()
	}
	res, err := sessions.Build(p.H, in)
	if err != nil {
		return nil, err
	}
	if len(res.Resumed) > 0 {
		_ = p.Store.Remove(res.Resumed...)
	}
	return res.Rows, nil
}

// Find resolves a target to one row: a pane id, a session id (or a prefix of
// at least 8 characters), or a session name.
func Find(rows []sessions.Row, target string) (sessions.Row, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return sessions.Row{}, errors.New("no session given")
	}
	var byName []sessions.Row
	for _, r := range rows {
		switch {
		case r.PaneID == target && !r.Parked():
			return r, nil
		case r.SessionID != "" && (r.SessionID == target || (len(target) >= 8 && strings.HasPrefix(r.SessionID, target))):
			return r, nil
		case r.Name == target:
			byName = append(byName, r)
		}
	}
	// A pane id can also name a parked session whose pane is now a shell.
	for _, r := range rows {
		if r.PaneID == target && r.Parked() {
			return r, nil
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return sessions.Row{}, fmt.Errorf("no session matches %q", target)
	default:
		ids := make([]string, len(byName))
		for i, r := range byName {
			ids[i] = r.PaneID
		}
		return sessions.Row{}, fmt.Errorf("%d sessions are named %q (panes %s); use a pane id", len(byName), target, strings.Join(ids, ", "))
	}
}

// Idle returns the sessions that can be parked and have been inactive for at
// least d, skipping the session in pane self (the one asking).
func Idle(rows []sessions.Row, now time.Time, d time.Duration, self string) []sessions.Row {
	var out []sessions.Row
	for _, r := range rows {
		if r.Ready() && r.PaneID != self && r.SessionID != "" && r.IdleFor(now) >= d {
			out = append(out, r)
		}
	}
	return out
}

// ParkAll parks each row in turn and keeps going past failures, which it
// reports together.
//
// rows may be minutes old (a confirmation prompt, a long batch), so each one
// is looked up again first and skipped if it was used since.
func (p *Parker) ParkAll(rows []sessions.Row) (parked int, err error) {
	fresh, err := p.byID()
	if err != nil {
		return 0, err
	}
	var failed []string
	for _, r := range rows {
		f, ok := fresh[r.SessionID]
		if !ok || f.Parked() || f.LastActive.After(r.LastActive) {
			failed = append(failed, r.Name+" (used again)")
			continue
		}
		if e := p.Park(f, ParkOptions{}); e != nil {
			failed = append(failed, r.Name)
			continue
		}
		parked++
	}
	if len(failed) > 0 {
		return parked, fmt.Errorf("parked %d; skipped: %s", parked, strings.Join(failed, ", "))
	}
	return parked, nil
}

// byID lists sessions now, keyed by session id.
func (p *Parker) byID() (map[string]sessions.Row, error) {
	rows, err := p.List()
	if err != nil {
		return nil, err
	}
	m := make(map[string]sessions.Row, len(rows))
	for _, r := range rows {
		if r.SessionID != "" {
			m[r.SessionID] = r
		}
	}
	return m, nil
}

// Resumable returns the parked sessions whose agent has exited (not the ones
// still pending), in the order given.
func Resumable(rows []sessions.Row) []sessions.Row {
	var out []sessions.Row
	for _, r := range rows {
		if r.Parked() && !r.Pending {
			out = append(out, r)
		}
	}
	return out
}

// UnparkAll resumes each row in turn and keeps going past failures, which it
// reports together.
func (p *Parker) UnparkAll(rows []sessions.Row) (resumed int, err error) {
	fresh, err := p.byID()
	if err != nil {
		return 0, err
	}
	var failed []string
	for _, r := range rows {
		f, ok := fresh[r.SessionID]
		if !ok || !f.Parked() {
			continue // resumed meanwhile
		}
		if _, e := p.Unpark(f); e != nil {
			failed = append(failed, r.Name)
			continue
		}
		resumed++
	}
	if len(failed) > 0 {
		return resumed, fmt.Errorf("resumed %d; failed: %s", resumed, strings.Join(failed, ", "))
	}
	return resumed, nil
}

// ParkOptions changes Park's safety checks.
type ParkOptions struct {
	// Force parks a session even when herdr reports it as working or unknown.
	Force bool
}

// InTab returns the live sessions in tab, skipping pane self: the ones that
// can be parked, and the ones that are busy (working, blocked or unknown).
func InTab(rows []sessions.Row, tab, self string) (ready, busy []sessions.Row) {
	for _, r := range rows {
		switch {
		case r.Parked() || r.TabID != tab || r.PaneID == self:
		case r.Ready() && r.SessionID != "":
			ready = append(ready, r)
		default:
			busy = append(busy, r)
		}
	}
	return ready, busy
}

// ParkedInTab returns the resumable parked sessions that were in tab.
func ParkedInTab(rows []sessions.Row, tab string) []sessions.Row {
	var out []sessions.Row
	for _, r := range Resumable(rows) {
		if r.TabID == tab {
			out = append(out, r)
		}
	}
	return out
}

// CanPark reports why row can't be parked, or nil. Park checks the pane
// again before acting; this is the part that needs no herdr call.
func CanPark(row sessions.Row, opt ParkOptions) error {
	switch {
	case row.Parked():
		return fmt.Errorf("%s is already parked", row.Name)
	case row.Status == sessions.StatusBlocked:
		return fmt.Errorf("%s is waiting on a question or approval; answer it first", row.Name)
	case !row.Ready() && !opt.Force:
		return fmt.Errorf("%s is %s; park it once it is idle, or force it (F in the panel, --force on the CLI)", row.Name, row.Status)
	case row.SessionID == "":
		return fmt.Errorf("%s has no known session id, so it could not be resumed; not parking it", row.Name)
	case !validSessionID(row.SessionID):
		return fmt.Errorf("%s has an unexpected session id %q; not parking it", row.Name, row.SessionID)
	case !safeCwd(row.Cwd):
		return fmt.Errorf("%s runs in a folder name with control characters; not parking it", row.Name)
	}
	if _, ok := sessions.Agents[cmp.Or(row.Kind, sessions.AgentKind)]; !ok {
		return fmt.Errorf("%s: avocado-park can't park %s agents", row.Name, row.Kind)
	}
	return nil
}

// CanUnpark reports why row can't be resumed, or nil.
func CanUnpark(row sessions.Row) error {
	switch {
	case !row.Parked():
		return fmt.Errorf("%s is not parked", row.Name)
	case row.Pending:
		return fmt.Errorf("%s is still exiting; try again in a moment", row.Name)
	case !validSessionID(row.SessionID):
		return fmt.Errorf("%s has an unexpected session id %q; resume it by hand", row.Name, row.SessionID)
	case !safeCwd(row.Cwd):
		return fmt.Errorf("%s has a folder name with control characters; resume it by hand", row.Name)
	}
	if _, ok := sessions.Agents[cmp.Or(row.Kind, sessions.AgentKind)]; !ok {
		return fmt.Errorf("%s: avocado-park can't resume %s agents", row.Name, row.Kind)
	}
	return nil
}

// CanClose reports why row can't be closed, or nil. Close checks the pane
// again before acting; this is the part that needs no herdr call.
func CanClose(row sessions.Row) error {
	switch {
	case row.Pending:
		return fmt.Errorf("%s is still exiting; try again in a moment", row.Name)
	case row.Parked():
		return nil
	case row.Status == sessions.StatusBlocked:
		return fmt.Errorf("%s is waiting on a question or approval; answer it first", row.Name)
	case row.Status == sessions.StatusWorking:
		return fmt.Errorf("%s is working; close it once it is idle", row.Name)
	}
	if _, ok := sessions.Agents[cmp.Or(row.Kind, sessions.AgentKind)]; !ok {
		return fmt.Errorf("%s: avocado-park can't close %s agents", row.Name, row.Kind)
	}
	return nil
}

// Close ends a session for good: it exits the agent if one runs, closes the
// pane and forgets the session. Nothing is kept to resume it from, though the
// agent's own conversation history stays on disk.
func (p *Parker) Close(row sessions.Row) error {
	if err := CanClose(row); err != nil {
		return err
	}
	unlock, err := p.Store.Lock()
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	if row.Parked() {
		if row.PaneExists {
			if err := p.closeShell(row); err != nil {
				return err
			}
		}
		return p.Store.Remove(row.SessionID)
	}

	// The row may be seconds old: check the pane again before exiting the
	// agent, as Park does.
	kind := cmp.Or(row.Kind, sessions.AgentKind)
	pane, err := p.H.Pane(row.PaneID)
	if err != nil {
		return fmt.Errorf("check %s: %w", row.Name, err)
	}
	switch {
	case pane.Agent != kind:
		return fmt.Errorf("%s: pane %s no longer runs %s", row.Name, row.PaneID, kind)
	case pane.SessionID() != row.AgentRef:
		return fmt.Errorf("%s: pane %s now runs a different session; refresh and try again", row.Name, row.PaneID)
	case pane.AgentStatus == sessions.StatusBlocked:
		return fmt.Errorf("%s is waiting on a question or approval; answer it first", row.Name)
	case pane.AgentStatus == sessions.StatusWorking:
		return fmt.Errorf("%s is working; close it once it is idle", row.Name)
	}
	if err := p.H.Prompt(row.PaneID, sessions.Agents[kind].Exit); err != nil {
		return fmt.Errorf("send %s to %s: %w", sessions.Agents[kind].Exit, row.Name, err)
	}
	if err := p.waitExited(row); err != nil {
		return fmt.Errorf("asked %s to exit but it is still running after %s; its pane stays open", row.Name, p.ExitTimeout)
	}
	if err := p.closeShell(row); err != nil {
		return err
	}
	if row.SessionID == "" {
		return nil
	}
	return p.Store.Remove(row.SessionID)
}

// closeShell closes row's pane, which must be at a shell prompt: another
// program running there is work this session doesn't own. A pane that is
// already gone counts as closed.
func (p *Parker) closeShell(row sessions.Row) error {
	info, err := p.H.Pane(row.PaneID)
	if herdr.IsCode(err, "pane_not_found") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check pane %s: %w", row.PaneID, err)
	}
	if info.Agent != "" {
		return fmt.Errorf("pane %s now runs %s; leaving it open", row.PaneID, info.Agent)
	}
	// After a herdr restart the id can name a pane elsewhere.
	if row.WorkspaceID != "" && info.WorkspaceID != row.WorkspaceID {
		return fmt.Errorf("pane %s is now in another workspace; leaving it open", row.PaneID)
	}
	busy, _, known := p.foreground(row.PaneID)
	switch {
	case busy != "":
		return fmt.Errorf("pane %s is running %s; leaving it open", row.PaneID, busy)
	case !known:
		return fmt.Errorf("can't tell what pane %s is running; leaving it open", row.PaneID)
	}
	if err := p.H.ClosePane(row.PaneID); err != nil && !herdr.IsCode(err, "pane_not_found") {
		return fmt.Errorf("close pane %s: %w", row.PaneID, err)
	}
	return nil
}

// Park exits the agent in row's pane and records how to resume it.
func (p *Parker) Park(row sessions.Row, opt ParkOptions) error {
	if err := CanPark(row, opt); err != nil {
		return err
	}
	kind := cmp.Or(row.Kind, sessions.AgentKind)
	agent := sessions.Agents[kind]
	unlock, err := p.Store.Lock()
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	// The row may be seconds old (the panel refreshes every few seconds):
	// check the pane again right before sending the exit command.
	pane, err := p.H.Pane(row.PaneID)
	if err != nil {
		return fmt.Errorf("check %s: %w", row.Name, err)
	}
	switch {
	case pane.Agent != kind:
		return fmt.Errorf("%s: pane %s no longer runs %s", row.Name, row.PaneID, kind)
	case pane.SessionID() != row.AgentRef:
		return fmt.Errorf("%s: pane %s now runs a different session; refresh and try again", row.Name, row.PaneID)
	case pane.AgentStatus == sessions.StatusBlocked:
		return fmt.Errorf("%s is waiting on a question or approval; answer it first", row.Name)
	case pane.AgentStatus != sessions.StatusIdle && pane.AgentStatus != sessions.StatusDone && !opt.Force:
		return fmt.Errorf("%s is %s; park it once it is idle (or force it)", row.Name, pane.AgentStatus)
	}

	entry := state.Entry{
		SessionID: row.SessionID, Name: row.Name, Kind: kind, PaneID: row.PaneID, TabID: row.TabID,
		WorkspaceID: row.WorkspaceID, Workspace: row.Workspace, Cwd: row.Cwd, ParkedAt: p.Now(),
		LastActive: row.LastActive, FreedBytes: row.RSSBytes, Pending: true,
	}
	return p.exit(row, agent, entry)
}

func (p *Parker) exit(row sessions.Row, agent sessions.Agent, entry state.Entry) error {
	// Record first: if anything below fails half way, the session id is safe.
	if err := p.Store.Put(entry); err != nil {
		return fmt.Errorf("save parked session: %w", err)
	}
	if err := p.H.Prompt(row.PaneID, agent.Exit); err != nil {
		_ = p.Store.Remove(row.SessionID)
		return fmt.Errorf("send %s to %s: %w", agent.Exit, row.Name, err)
	}
	if err := p.waitExited(row); err != nil {
		return err
	}
	entry.Pending = false
	return p.Store.Put(entry)
}

func (p *Parker) waitExited(row sessions.Row) error {
	deadline := p.Now().Add(p.ExitTimeout)
	for {
		pane, err := p.H.Pane(row.PaneID)
		paneGone := herdr.IsCode(err, "pane_not_found")
		agentGone := paneGone || (err == nil && pane.Agent == "")
		procGone := row.PID == 0 || !p.Alive(row.PID)
		if agentGone && procGone {
			return nil
		}
		if !p.Now().Before(deadline) {
			return fmt.Errorf("asked %s to exit but it is still running after %s; it stays listed as parked (pending)", row.Name, p.ExitTimeout)
		}
		p.Sleep(250 * time.Millisecond)
	}
}

// Unpark resumes a parked session, in its original pane when that pane is
// still an idle shell, otherwise in a new tab in the same workspace. It
// returns the pane the session runs in.
func (p *Parker) Unpark(row sessions.Row) (string, error) {
	if err := CanUnpark(row); err != nil {
		return "", err
	}
	unlock, err := p.Store.Lock()
	if err != nil {
		return "", fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	// row may be stale: another process may have resumed (or be resuming)
	// the session while this one waited for the lock.
	entries, err := p.Store.Load()
	if err != nil {
		return "", fmt.Errorf("read %s: %w", p.Store.Path, err)
	}
	if !slices.ContainsFunc(entries, func(e state.Entry) bool { return e.SessionID == row.SessionID }) {
		return "", fmt.Errorf("%s is no longer parked", row.Name)
	}
	agents, err := p.H.Agents()
	if err != nil {
		return "", fmt.Errorf("list agents: %w", err)
	}
	for _, a := range agents {
		if a.SessionID() == row.SessionID {
			return "", fmt.Errorf("%s is already running in pane %s", row.Name, a.PaneID)
		}
	}

	kind := cmp.Or(row.Kind, sessions.AgentKind)
	agent := sessions.Agents[kind]

	// Reuse the original pane only when it is still the same pane (after a
	// herdr restart the id can name a pane elsewhere; List checked its
	// workspace label) and sits at a shell.
	pane := row.PaneID
	info, err := p.H.Pane(pane)
	if err != nil && !herdr.IsCode(err, "pane_not_found") {
		return "", fmt.Errorf("check pane %s: %w", pane, err)
	}
	reuse := err == nil && row.PaneExists && info.Agent == "" && (row.WorkspaceID == "" || info.WorkspaceID == row.WorkspaceID)
	if reuse {
		busy, posix, known := p.foreground(pane)
		if busy != "" {
			return "", fmt.Errorf("pane %s is running %s; return it to the shell prompt first", pane, busy)
		}
		// Can't tell what runs there: don't type into it. A non-POSIX shell
		// may not honour shellQuote, so a needed cd goes through a new tab
		// (whose folder is set through the API) instead.
		needCd := row.Cwd != "" && info.ForegroundCwd != row.Cwd
		reuse = known && (posix || !needCd)
	}
	if !reuse {
		if pane, err = p.H.CreateTab(row.WorkspaceID, row.Cwd, row.Name); err != nil {
			// The workspace may be gone too: fall back to the focused one.
			if pane, err = p.H.CreateTab("", row.Cwd, row.Name); err != nil {
				return "", fmt.Errorf("open a tab for %s: %w", row.Name, err)
			}
		}
	} else if info.ForegroundCwd != row.Cwd && row.Cwd != "" {
		// Claude finds a conversation by the folder it started in.
		if err := p.H.RunInPane(pane, "cd "+shellQuote(row.Cwd)); err != nil {
			return "", fmt.Errorf("cd in %s: %w", pane, err)
		}
		p.Sleep(300 * time.Millisecond)
	}

	args := agent.Resume(row.SessionID)
	name := AgentName(row.Name)
	err = p.H.StartAgent(name, kind, pane, args, p.StartTimeout)
	if err != nil && strings.Contains(err.Error(), "name") && p.paneIdle(pane) {
		// Another live agent may already use this name. Retry only when
		// nothing started, so a second resume is never typed into a running one.
		err = p.H.StartAgent(AgentName(row.Name+"-"+row.SessionID[:min(4, len(row.SessionID))]), kind, pane, args, p.StartTimeout)
	}
	if err != nil {
		return pane, fmt.Errorf("resume %s in %s: %w", row.Name, pane, err)
	}
	return pane, p.Store.Remove(row.SessionID)
}

var (
	nameUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)
	sessionRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

// validSessionID rejects ids that could pass as a flag or carry anything
// besides an id when handed to `claude --resume` / `codex resume`.
func validSessionID(id string) bool { return sessionRe.MatchString(id) }

// safeCwd rejects folder names that aren't valid UTF-8 or hold control
// characters (C0, DEL, C1): the folder is typed into the pane as
// `cd '<dir>'`, and the terminal would act on them as keys.
func safeCwd(dir string) bool {
	return utf8.ValidString(dir) &&
		!strings.ContainsFunc(dir, func(r rune) bool { return r < 0x20 || (r >= 0x7f && r <= 0x9f) })
}

// shells are the foreground programs a pane may run for avocado-park to type
// into it. true marks POSIX shells, where shellQuote's quoting holds; in the
// others (fish, nu, csh, …) avocado-park types nothing but the agent start.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
	"fish": false, "tcsh": false, "csh": false, "nu": false, "elvish": false, "xonsh": false}

// foreground returns the name of a non-shell program in the pane's
// foreground, or "" when it sits at a shell prompt. posix reports whether
// every shell there is a POSIX one; known is false when herdr can't tell
// (an error or no processes).
func (p *Parker) foreground(pane string) (busy string, posix, known bool) {
	procs, err := p.H.ForegroundProcesses(pane)
	if err != nil || len(procs) == 0 {
		return "", false, false
	}
	posix = true
	for _, pr := range procs {
		name := strings.TrimPrefix(filepath.Base(cmp.Or(pr.Argv0, pr.Name)), "-") // login shells: -zsh
		isPOSIX, isShell := shells[name]
		if name != "" && !isShell {
			return name, false, true
		}
		posix = posix && isPOSIX
	}
	return "", posix, true
}

// paneIdle reports whether no agent runs in the pane.
func (p *Parker) paneIdle(pane string) bool {
	info, err := p.H.Pane(pane)
	return err == nil && info.Agent == ""
}

// AgentName turns a session name into a valid herdr agent name:
// [a-z][a-z0-9_-]{0,31}.
func AgentName(s string) string {
	s = strings.Trim(nameUnsafe.ReplaceAllString(strings.ToLower(s), "-"), "-_")
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "claude-" + s
	}
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-_")
	}
	return s
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
