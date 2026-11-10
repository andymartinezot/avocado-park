// Package tui is the avocado-park panel: a live list of agent sessions where
// each one can be parked, unparked or jumped to.
package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/andymartinezot/avocado-park/internal/park"
	"github.com/andymartinezot/avocado-park/internal/sessions"
)

// Options configures the panel.
type Options struct {
	// Self is the pane the panel was opened from; it is never parked from here.
	Self     string
	IdleDays int
	Refresh  time.Duration
	// Theme is an avocado theme id (see ThemeNames); empty picks one with PickTheme.
	Theme string
}

type model struct {
	p    *park.Parker
	opt  Options
	all  []sessions.Row // every session from the last refresh
	rows []sessions.Row // the ones matching filter, as shown
	cur  int
	w, h int

	filter    string // case-insensitive match on name, workspace, tab and pane
	filtering bool   // typing into the filter
	ws        string // the only workspace shown (its label); empty shows all

	busy    string // non-empty while an action runs
	msg     string
	msgErr  bool
	confirm []sessions.Row // sessions the pending action would act on, awaiting y/n
	action  string         // the pending action: one of the act* constants
	loaded  bool
}

type rowsMsg struct {
	rows []sessions.Row
	err  error
}
type doneMsg struct {
	text string
	err  error
	quit bool
}
type tickMsg struct{}

// Run shows the panel until the user quits.
func Run(p *park.Parker, opt Options) error {
	applyTheme(opt.Theme)
	_, err := tea.NewProgram(model{p: p, opt: opt}, tea.WithAltScreen()).Run()
	return err
}

// Render returns one frame of the panel at the given size, for screenshots
// and for checking the layout without a terminal.
func Render(p *park.Parker, opt Options, width, height int) (string, error) {
	rows, err := p.List()
	if err != nil {
		return "", err
	}
	applyTheme(opt.Theme)
	m := model{p: p, opt: opt, w: width, h: height, loaded: true}
	m.setRows(rows)
	return m.View(), nil
}

func (m model) Init() tea.Cmd { return tea.Batch(m.load(), m.tick()) }

func (m model) load() tea.Cmd {
	return func() tea.Msg {
		rows, err := m.p.List()
		return rowsMsg{rows, err}
	}
}

func (m model) tick() tea.Cmd {
	return tea.Tick(m.tickEvery(), func(time.Time) tea.Msg { return tickMsg{} })
}

// busyRefresh is the longest wait between refreshes while an action runs, so
// a batch shows each session changing instead of only the end result.
const busyRefresh = time.Second

func (m model) tickEvery() time.Duration {
	if m.busy != "" {
		return min(m.opt.Refresh, busyRefresh)
	}
	return m.opt.Refresh
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tickMsg:
		return m, tea.Batch(m.load(), m.tick())
	case rowsMsg:
		m.loaded = true
		if msg.err != nil {
			m.setMsg(msg.err.Error(), true)
			return m, nil
		}
		m.setRows(msg.rows)
	case doneMsg:
		m.busy = ""
		if msg.err != nil {
			m.setMsg(msg.err.Error(), true)
		} else {
			m.setMsg(msg.text, false)
		}
		if msg.quit && msg.err == nil {
			return m, tea.Quit
		}
		return m, m.load()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *model) setMsg(s string, isErr bool) { m.msg, m.msgErr = s, isErr }

// rowKey identifies a session across refreshes, which reorder the list.
func rowKey(r sessions.Row) string {
	if r.SessionID != "" {
		return r.SessionID
	}
	return r.PaneID
}

// setRows replaces the session list and keeps the cursor on the session it
// was on, so a refresh never moves an action to a different session.
func (m *model) setRows(all []sessions.Row) {
	m.all = all
	m.refilter()
}

// refilter recomputes the shown rows from the filter, keeping the selection.
func (m *model) refilter() {
	var key string
	if m.cur < len(m.rows) {
		key = rowKey(m.rows[m.cur])
	}
	q := strings.ToLower(m.filter)
	m.rows = m.rows[:0:0]
	for _, r := range m.all {
		if m.ws != "" && r.Workspace != m.ws {
			continue
		}
		if q == "" || slices.ContainsFunc([]string{r.Name, r.Workspace, r.Tab, r.PaneLabel}, func(s string) bool {
			return strings.Contains(strings.ToLower(s), q)
		}) {
			m.rows = append(m.rows, r)
		}
	}
	m.cur = min(m.cur, max(0, len(m.rows)-1))
	for i, r := range m.rows {
		if key != "" && rowKey(r) == key {
			m.cur = i
			break
		}
	}
}

// nextWorkspace moves the workspace filter by step through all, then each
// workspace with sessions in name order, wrapping around.
func (m *model) nextWorkspace(step int) {
	choices := []string{""}
	for _, r := range m.all {
		if r.Workspace != "" && !slices.Contains(choices, r.Workspace) {
			choices = append(choices, r.Workspace)
		}
	}
	slices.SortFunc(choices[1:], func(a, b string) int {
		return strings.Compare(strings.ToLower(workspace(a)), strings.ToLower(workspace(b)))
	})
	// A workspace that has no sessions any more counts as all.
	i := max(0, slices.Index(choices, m.ws))
	m.ws = choices[(i+step+len(choices))%len(choices)]
	m.refilter()
}

// Actions that ask y/n first.
const (
	actParkIdle  = "park-idle"
	actParkTab   = "park-tab"
	actUnparkAll = "unpark-all"
	actForce     = "force"
	actClose     = "close"
)

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirm != nil {
		switch k.String() {
		case "y", "Y":
			batch := m.confirm
			m.confirm = nil
			switch m.action {
			case actUnparkAll:
				m.busy = fmt.Sprintf("resuming %d sessions…", len(batch))
				return m, m.unparkAll(batch)
			case actForce:
				row := batch[0]
				m.busy = "parking " + row.Name + "…"
				return m, m.run(func() (string, error) {
					return "parked " + row.Name, m.p.Park(row, park.ParkOptions{Force: true})
				}, false)
			case actClose:
				row := batch[0]
				m.busy = "closing " + row.Name + "…"
				return m, m.run(func() (string, error) {
					return "closed " + row.Name, m.p.Close(row)
				}, false)
			}
			m.busy = fmt.Sprintf("parking %d sessions…", len(batch))
			return m, m.parkAll(batch)
		default:
			m.confirm = nil
			m.setMsg("cancelled", false)
		}
		return m, nil
	}
	if m.filtering {
		switch k.Type {
		case tea.KeyEnter:
			m.filtering = false
		case tea.KeyEsc:
			m.filtering, m.filter = false, ""
			m.refilter()
		case tea.KeyBackspace:
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
				m.refilter()
			}
		case tea.KeyRunes, tea.KeySpace:
			m.filter += string(k.Runes)
			m.refilter()
		case tea.KeyUp, tea.KeyDown:
			m.filtering = false
			return m.key(k)
		case tea.KeyCtrlC:
			return m, tea.Quit
		}
		return m, nil
	}
	switch k.String() {
	case "/":
		m.filtering = true
		return m, nil
	case "w":
		m.nextWorkspace(1)
		return m, nil
	case "W":
		m.nextWorkspace(-1)
		return m, nil
	case "esc":
		switch {
		case m.filter != "":
			m.filter = ""
		case m.ws != "":
			m.ws = ""
		default:
			return m, tea.Quit
		}
		m.refilter()
		return m, nil
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.cur = max(0, m.cur-1)
	case "down", "j":
		m.cur = min(len(m.rows)-1, m.cur+1)
	case "home", "g":
		m.cur = 0
	case "end", "G":
		m.cur = max(0, len(m.rows)-1)
	case "r":
		return m, m.load()
	}
	if m.busy != "" || len(m.rows) == 0 {
		return m, nil
	}
	row := m.rows[m.cur]
	switch k.String() {
	case "p":
		if row.PaneID == m.opt.Self && !row.Parked() {
			m.setMsg("that's the pane you opened this panel from; park it from another pane", true)
			return m, nil
		}
		m.busy = "parking " + row.Name + "…"
		return m, m.run(func() (string, error) {
			return "parked " + row.Name, m.p.Park(row, park.ParkOptions{})
		}, false)
	case "u":
		m.busy = "resuming " + row.Name + "…"
		return m, m.run(func() (string, error) {
			pane, err := m.p.Unpark(row)
			return fmt.Sprintf("resumed %s in %s", row.Name, pane), err
		}, false)
	case "x":
		if row.PaneID == m.opt.Self && !row.Parked() {
			m.setMsg("that's the pane you opened this panel from; close it from another pane", true)
			return m, nil
		}
		if err := park.CanClose(row); err != nil {
			m.setMsg(err.Error(), true)
			return m, nil
		}
		m.confirm, m.action = []sessions.Row{row}, actClose
	case "enter":
		if row.Parked() && !row.PaneExists {
			m.setMsg("its pane was closed; press u to resume it in a new tab", true)
			return m, nil
		}
		return m, m.run(func() (string, error) { return "", m.p.H.FocusPane(row.PaneID) }, true)
	case "P":
		batch := park.Idle(m.rows, m.p.Now(), time.Duration(m.opt.IdleDays)*24*time.Hour, m.opt.Self)
		if len(batch) == 0 {
			m.setMsg(fmt.Sprintf("no sessions idle for %dd or more", m.opt.IdleDays), false)
			return m, nil
		}
		m.confirm, m.action = batch, actParkIdle
	case "T":
		// Every idle session in the selected session's tab, from all
		// sessions rather than the filtered ones: a tab is one unit.
		batch, _ := park.InTab(m.all, row.TabID, m.opt.Self)
		if row.Parked() || len(batch) == 0 {
			m.setMsg("no idle sessions to park in that tab", false)
			return m, nil
		}
		m.confirm, m.action = batch, actParkTab
	case "U":
		batch := park.Resumable(m.rows)
		if len(batch) == 0 {
			m.setMsg("no parked sessions", false)
			return m, nil
		}
		m.confirm, m.action = batch, actUnparkAll
	case "F":
		// For agents herdr can't read as idle (Codex often shows "unknown").
		switch {
		case row.Parked():
			m.setMsg(row.Name+" is already parked", true)
		case row.PaneID == m.opt.Self:
			m.setMsg("that's the pane you opened this panel from; park it from another pane", true)
		case row.Status == sessions.StatusBlocked:
			m.setMsg(row.Name+" is waiting on a question or approval; answer it first", true)
		default:
			m.confirm, m.action = []sessions.Row{row}, actForce
		}
	}
	return m, nil
}

func (m model) run(fn func() (string, error), quit bool) tea.Cmd {
	return func() tea.Msg {
		text, err := fn()
		return doneMsg{text: text, err: err, quit: quit}
	}
}

func (m model) parkAll(batch []sessions.Row) tea.Cmd {
	return m.run(func() (string, error) {
		n, err := m.p.ParkAll(batch)
		return fmt.Sprintf("parked %d sessions", n), err
	}, false)
}

func (m model) unparkAll(batch []sessions.Row) tea.Cmd {
	return m.run(func() (string, error) {
		n, err := m.p.UnparkAll(batch)
		return fmt.Sprintf("resumed %d sessions", n), err
	}, false)
}

// ---- view ----

const (
	wStatus = 10
	wIdle   = 6
	wRAM    = 6
	minName = 14
	maxName = 40
	minWS   = 10
	minTab  = 8
	minPane = 8
	gap     = 2 // before the tab and pane columns
)

// widths are the flexible columns. tab and pane are 0 when hidden.
type widths struct{ name, ws, tab, pane int }

// fitColumns sizes the flexible columns to fill room. Each starts at the
// width of its content; when that doesn't fit, the widest gives way first.
// Too narrow for all four, pane and then tab are hidden.
func fitColumns(rows []sessions.Row, room int) widths {
	var w widths
	for _, r := range rows {
		w.name = max(w.name, lipgloss.Width(sessions.Printable(r.Name)))
		w.ws = max(w.ws, lipgloss.Width(wsName(r)))
		w.tab = max(w.tab, lipgloss.Width(sessions.Printable(r.Tab)))
		w.pane = max(w.pane, lipgloss.Width(sessions.Printable(r.PaneLabel)))
	}
	w.name = min(max(w.name, minName), maxName)
	w.ws, w.tab, w.pane = max(w.ws, minWS), max(w.tab, minTab), max(w.pane, minPane)
	cols := []struct {
		w   *int
		min int
	}{{&w.name, minName}, {&w.ws, minWS}, {&w.tab, minTab}, {&w.pane, minPane}}
	switch {
	case room >= minName+minWS+minTab+minPane+2*gap:
		room -= 2 * gap
	case room >= minName+minWS+minTab+gap:
		w.pane, cols = 0, cols[:3]
		room -= gap
	default:
		w.tab, w.pane, cols = 0, 0, cols[:2]
	}
	used := 0
	for _, c := range cols {
		used += *c.w
	}
	for ; used > room; used-- {
		widest := -1
		for i, c := range cols {
			if *c.w > c.min && (widest < 0 || *c.w > *cols[widest].w) {
				widest = i
			}
		}
		if widest < 0 {
			break
		}
		*cols[widest].w--
	}
	*cols[len(cols)-1].w += max(0, room-used) // the last column takes what's left
	return w
}

// wsName is the row's workspace as shown.
func wsName(r sessions.Row) string { return workspace(sessions.Printable(r.Workspace)) }

// orDash marks a tab or pane without a name.
func orDash(s string) string {
	if s = sessions.Printable(s); s != "" {
		return s
	}
	return "-"
}

func (m model) View() string {
	if !m.loaded {
		return "\n  loading sessions…"
	}
	// The box fills the window. Row = cursor(2) + gaps(5) + fixed columns +
	// flexible columns; the box adds 4 (border + padding).
	fixed := 2 + 5 + wStatus + wIdle + wRAM
	inner := max(fixed+minName+minWS, m.w-4)
	w := fitColumns(m.rows, inner-fixed)
	now := m.p.Now()

	var b strings.Builder
	b.WriteString(titleSt.Render("avocado-park") + dimSt.Render("  Claude Code, Codex and OpenCode sessions"))
	if m.ws != "" {
		b.WriteString("   " + keySt.Render("w") + " " + ansi.Truncate(workspace(sessions.Printable(m.ws)), max(8, inner-60), "…"))
	}
	if m.filtering || m.filter != "" {
		cursor := ""
		if m.filtering {
			cursor = "▏"
		}
		b.WriteString("   " + keySt.Render("/") + " " + ansi.Truncate(m.filter, max(8, inner-40), "…") + cursor)
	}
	b.WriteString("\n\n")
	b.WriteString(headSt.Render("  " + pad("SESSION", w.name) + " " + pad("STATUS", wStatus) + " " + padLeft("IDLE", wIdle) + " " +
		padLeft("RAM", wRAM) + "  " + w.places("WORKSPACE", "TAB", "PANE")))
	b.WriteString("\n")

	// Keep the cursor visible when there are more rows than lines. Besides
	// the rows: 3 header lines, 4 footer lines, 2 border lines.
	avail := max(3, m.h-9)
	start := 0
	if m.cur >= avail {
		start = m.cur - avail + 1
	}
	var liveN, parkedN int
	var liveRAM, freed int64
	for _, r := range m.all {
		if r.Parked() {
			parkedN++
			freed += r.FreedBytes
		} else {
			liveN++
			liveRAM += r.RSSBytes
		}
	}
	if len(m.rows) == 0 {
		empty := "  no Claude Code, Codex or OpenCode sessions in herdr"
		switch {
		case m.filter != "":
			empty = "  no sessions match " + strconv.Quote(m.filter)
		case m.ws != "":
			empty = "  no sessions in " + workspace(sessions.Printable(m.ws))
		}
		b.WriteString(dimSt.Render(empty) + "\n")
	}
	for i := start; i < len(m.rows) && i < start+avail; i++ {
		r := m.rows[i]
		name := sessions.Printable(r.Name)
		idle := "-"
		if r.Parked() {
			idle = sessions.FormatAge(now.Sub(cmpTime(r.LastActive, r.ParkedAt)))
		} else if d := r.IdleFor(now); d > 0 {
			idle = sessions.FormatAge(d)
		}
		ram := "-"
		if r.RSSBytes > 0 {
			ram = sessions.FormatBytes(r.RSSBytes)
		}
		ramCell := padLeft(ram, wRAM)
		if r.Parked() && r.FreedBytes > 0 {
			ramCell = dimSt.Render(padLeft("("+sessions.FormatBytes(r.FreedBytes)+")", wRAM)) // memory freed
		}
		nameCell := pad(name, w.name)
		idleCell := padLeft(idle, wIdle)
		if !r.Parked() && r.Ready() && r.IdleFor(now) >= time.Duration(m.opt.IdleDays)*24*time.Hour {
			idleCell = staleSt.Render(idleCell) // what P would park
		}
		rest := idleCell + " " + ramCell + "  " + w.places(wsName(r), orDash(r.Tab), orDash(r.PaneLabel))
		cursor := "  "
		if r.PaneID == m.opt.Self && !r.Parked() {
			cursor = dimSt.Render("• ") // the pane the panel was opened from
		}
		if i == m.cur {
			cursor = keySt.Render("▸ ")
			nameCell = selSt.Render(nameCell)
		}
		line := cursor + nameCell + " " + statusCell(r) + rest
		if i == m.cur {
			line = paintSeq(line, selectSeq(), inner)
		}
		b.WriteString(line + "\n")
	}
	// Pad short lists so the footer sits at the bottom of the box.
	shown := min(len(m.rows)-start, avail)
	if len(m.rows) == 0 {
		shown = 1
	}
	b.WriteString(strings.Repeat("\n", max(0, avail-shown)))

	b.WriteString("\n")
	summary := fmt.Sprintf("%d live · %s   %d parked", liveN, sessions.FormatBytes(liveRAM), parkedN)
	if freed > 0 {
		summary += " · " + sessions.FormatBytes(freed) + " freed"
	}
	if slices.ContainsFunc(m.rows, paneGone) {
		summary += "   * pane gone: u resumes in a new tab"
	}
	b.WriteString(dimSt.Render(ansi.Truncate(summary, inner, "…")) + "\n")
	switch {
	case m.confirm != nil:
		names := make([]string, len(m.confirm))
		for i, r := range m.confirm {
			names[i] = sessions.Printable(r.Name)
		}
		q := fmt.Sprintf("Park %d sessions idle ≥%dd? ", len(m.confirm), m.opt.IdleDays)
		switch m.action {
		case actParkTab:
			q = fmt.Sprintf("Park %d sessions in this tab? ", len(m.confirm))
		case actUnparkAll:
			q = fmt.Sprintf("Resume %d parked sessions? ", len(m.confirm))
		case actForce:
			q = fmt.Sprintf("Force-park %s (%s)? Any reply in progress is lost. ", sessions.Printable(m.confirm[0].Name), m.confirm[0].Status)
		case actClose:
			b.WriteString(keySt.Render(ansi.Truncate(closeQuestion(m.confirm[0]), inner-5, "…")+"  y/n") + "\n")
		}
		if m.action != actClose {
			b.WriteString(keySt.Render(q) +
				ansi.Truncate(strings.Join(names, ", "), inner-30, "…") + keySt.Render("  y/n") + "\n")
		}
	case m.busy != "":
		b.WriteString(dimSt.Render(sessions.Printable(m.busy)) + "\n")
	case m.msg != "":
		st := okSt
		if m.msgErr {
			st = errSt
		}
		b.WriteString(st.Render(ansi.Truncate(sessions.Printable(m.msg), inner, "…")) + "\n")
	default:
		b.WriteString("\n")
	}
	help := []string{"p park", "u unpark", "x close", "⏎ go to pane", fmt.Sprintf("P park idle ≥%dd", m.opt.IdleDays), "T park tab", "U unpark all", "w workspace", "/ filter", "r refresh", "q quit"}
	if m.filtering {
		help = []string{"⏎ done", "esc clear", "↑↓ move"}
	}
	for i, h := range help {
		k, rest, _ := strings.Cut(h, " ")
		help[i] = keySt.Render(k) + " " + dimSt.Render(rest)
	}
	// Truncated rather than wrapped, so the frame never grows past the window.
	b.WriteString(ansi.Truncate(strings.Join(help, "  "), inner, "…"))

	return paint(boxSt.Width(inner+2).Render(b.String()), cur.surface, m.w)
}

// paneGone reports a parked session with no pane to return to, shown as
// parked*: its pane was closed, or now runs another agent.
func paneGone(r sessions.Row) bool { return r.Parked() && !r.Pending && !r.PaneExists }

// closeQuestion is what x asks before acting on r.
func closeQuestion(r sessions.Row) string {
	name := sessions.Printable(r.Name)
	switch {
	case paneGone(r):
		return "Remove " + name + " from the list? Its pane is already gone."
	case r.Parked():
		return "Close " + name + "'s pane and remove it from the list?"
	}
	return fmt.Sprintf("Exit %s (%s), close its pane and remove it from the list?", name, r.Status)
}

func statusCell(r sessions.Row) string {
	glyph, label := "○", r.Status
	switch r.Status {
	case sessions.StatusWorking:
		glyph = "●"
	case sessions.StatusBlocked:
		glyph = "◐"
	case sessions.StatusParked:
		glyph = "⏸"
		if r.Pending {
			label = "parking"
		} else if paneGone(r) {
			label = "parked*"
		}
	case sessions.StatusUnknown:
		glyph = "?"
	}
	cell := pad(glyph+" "+label, wStatus) + " "
	if st, ok := statusSty[r.Status]; ok {
		return st.Render(cell)
	}
	return dimSt.Render(cell)
}

// places renders the columns that say where a session is.
func (w widths) places(ws, tab, pane string) string {
	s := pad(ws, w.ws)
	if w.tab > 0 {
		s += strings.Repeat(" ", gap) + pad(tab, w.tab)
	}
	if w.pane > 0 {
		s += strings.Repeat(" ", gap) + pad(pane, w.pane)
	}
	return s
}

func pad(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

func padLeft(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return strings.Repeat(" ", max(0, w-lipgloss.Width(s))) + s
}

// workspace drops a leading "<emoji> - " decoration from herdr workspace labels.
func workspace(label string) string {
	if _, rest, ok := strings.Cut(label, " - "); ok && lipgloss.Width(label)-lipgloss.Width(rest) <= 5 {
		return rest
	}
	return label
}

func cmpTime(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	return a
}
