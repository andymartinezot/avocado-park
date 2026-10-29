// Package tui is the herdr-park panel: a live list of agent sessions where
// each one can be parked, unparked or jumped to.
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/andymartinezot/herdr-park/internal/park"
	"github.com/andymartinezot/herdr-park/internal/sessions"
)

// Options configures the panel.
type Options struct {
	// Self is the pane the panel was opened from; it is never parked from here.
	Self     string
	IdleDays int
	Refresh  time.Duration
}

type model struct {
	p    *park.Parker
	opt  Options
	all  []sessions.Row // every session from the last refresh
	rows []sessions.Row // the ones matching filter, as shown
	cur  int
	w, h int

	filter    string // case-insensitive match on name and workspace
	filtering bool   // typing into the filter

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
	return tea.Tick(m.opt.Refresh, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tickMsg:
		if m.busy == "" {
			return m, tea.Batch(m.load(), m.tick())
		}
		return m, m.tick()
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
		if q == "" || strings.Contains(strings.ToLower(r.Name), q) || strings.Contains(strings.ToLower(r.Workspace), q) {
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

// Actions that ask y/n first.
const (
	actParkIdle  = "park-idle"
	actParkTab   = "park-tab"
	actUnparkAll = "unpark-all"
	actForce     = "force"
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
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.refilter()
			return m, nil
		}
		return m, tea.Quit
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

var (
	accent    = lipgloss.AdaptiveColor{Light: "#5B4FC4", Dark: "#A99BFF"}
	dim       = lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#6E6E6E"}
	green     = lipgloss.AdaptiveColor{Light: "#1E8A4C", Dark: "#5FD787"}
	yellow    = lipgloss.AdaptiveColor{Light: "#A86A00", Dark: "#F2C14E"}
	blue      = lipgloss.AdaptiveColor{Light: "#2F6FB3", Dark: "#7AB8F5"}
	red       = lipgloss.AdaptiveColor{Light: "#B3261E", Dark: "#FF8A80"}
	purple    = lipgloss.AdaptiveColor{Light: "#9C3FC9", Dark: "#D08CFF"}
	titleSt   = lipgloss.NewStyle().Bold(true).Foreground(accent)
	headSt    = lipgloss.NewStyle().Foreground(dim).Bold(true)
	dimSt     = lipgloss.NewStyle().Foreground(dim)
	selSt     = lipgloss.NewStyle().Bold(true)
	keySt     = lipgloss.NewStyle().Foreground(accent).Bold(true)
	errSt     = lipgloss.NewStyle().Foreground(red)
	okSt      = lipgloss.NewStyle().Foreground(green)
	staleSt   = lipgloss.NewStyle().Foreground(purple)
	boxSt     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(dim).Padding(0, 1)
	statusSty = map[string]lipgloss.Style{
		sessions.StatusWorking: lipgloss.NewStyle().Foreground(green),
		sessions.StatusBlocked: lipgloss.NewStyle().Foreground(yellow),
		sessions.StatusParked:  lipgloss.NewStyle().Foreground(blue),
	}
)

const (
	wStatus = 10
	wIdle   = 6
	wRAM    = 6
	minWS   = 10
)

func (m model) View() string {
	if !m.loaded {
		return "\n  loading sessions…"
	}
	// The box fills the window. Row = cursor(2) + name + gaps(5) + fixed
	// columns + workspace; the box adds 4 (border + padding). The name column
	// fits the longest name (14..40) and the workspace takes the rest.
	fixed := 2 + 5 + wStatus + wIdle + wRAM
	inner := max(fixed+14+minWS, m.w-4)
	longest := 0
	for _, r := range m.rows {
		longest = max(longest, lipgloss.Width(sessions.Printable(r.Name)))
	}
	wName := min(max(longest, 14), 40, inner-fixed-minWS)
	wWS := inner - fixed - wName
	now := m.p.Now()

	var b strings.Builder
	b.WriteString(titleSt.Render("herdr-park") + dimSt.Render("  Claude Code, Codex and OpenCode sessions"))
	if m.filtering || m.filter != "" {
		cursor := ""
		if m.filtering {
			cursor = "▏"
		}
		b.WriteString("   " + keySt.Render("/") + " " + ansi.Truncate(m.filter, max(8, inner-40), "…") + cursor)
	}
	b.WriteString("\n\n")
	b.WriteString(headSt.Render("  " + cols(wName, wWS, "SESSION", "STATUS", "IDLE", "RAM", "WORKSPACE")))
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
		if m.filter != "" {
			empty = "  no sessions match " + strconv.Quote(m.filter)
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
		nameCell := pad(name, wName)
		idleCell := padLeft(idle, wIdle)
		if !r.Parked() && r.Ready() && r.IdleFor(now) >= time.Duration(m.opt.IdleDays)*24*time.Hour {
			idleCell = staleSt.Render(idleCell) // what P would park
		}
		rest := idleCell + " " + ramCell + "  " + pad(workspace(sessions.Printable(r.Workspace)), wWS)
		cursor := "  "
		if r.PaneID == m.opt.Self && !r.Parked() {
			cursor = dimSt.Render("• ") // the pane the panel was opened from
		}
		if i == m.cur {
			cursor = keySt.Render("▸ ")
			nameCell = selSt.Render(nameCell)
		}
		b.WriteString(cursor + nameCell + " " + statusCell(r) + rest + "\n")
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
	b.WriteString(dimSt.Render(summary) + "\n")
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
		}
		b.WriteString(keySt.Render(q) +
			ansi.Truncate(strings.Join(names, ", "), inner-30, "…") + keySt.Render("  y/n") + "\n")
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
	help := []string{"p park", "u unpark", "⏎ go to pane", fmt.Sprintf("P park idle ≥%dd", m.opt.IdleDays), "T park tab", "U unpark all", "/ filter", "r refresh", "q quit"}
	if m.filtering {
		help = []string{"⏎ done", "esc clear", "↑↓ move"}
	}
	for i, h := range help {
		k, rest, _ := strings.Cut(h, " ")
		help[i] = keySt.Render(k) + " " + dimSt.Render(rest)
	}
	// Truncated rather than wrapped, so the frame never grows past the window.
	b.WriteString(ansi.Truncate(strings.Join(help, "  "), inner, "…"))

	return boxSt.Width(inner + 2).Render(b.String())
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
		} else if !r.PaneExists {
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

func cols(wName, wWS int, name, status, idle, ram, ws string) string {
	return pad(name, wName) + " " + pad(status, wStatus) + " " + padLeft(idle, wIdle) + " " +
		padLeft(ram, wRAM) + "  " + pad(ws, wWS)
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
