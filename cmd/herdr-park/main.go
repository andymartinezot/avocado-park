// Command herdr-park lists Claude Code, Codex and OpenCode sessions running in herdr and parks
// (exits, keeping the pane) or unparks (resumes in place) them.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/andymartinezot/herdr-park/internal/config"
	"github.com/andymartinezot/herdr-park/internal/herdr"
	"github.com/andymartinezot/herdr-park/internal/park"
	"github.com/andymartinezot/herdr-park/internal/sessions"
	"github.com/andymartinezot/herdr-park/internal/state"
	"github.com/andymartinezot/herdr-park/internal/tui"
)

var version = "dev"

const usage = `herdr-park: park and resume Claude Code, Codex and OpenCode sessions without closing their herdr panes

Usage:
  herdr-park list [--json]              list live and parked sessions
  herdr-park park [--force] [TARGET]    exit the agent, keep the pane, remember the session
  herdr-park park --tab [TARGET]        park every idle session in a tab
  herdr-park park --idle [--days N]     park every session idle for N+ days (default park_idle_days)
  herdr-park unpark [TARGET]            resume a parked session in its pane
  herdr-park unpark --tab [TARGET]      resume every parked session of a tab
  herdr-park unpark --all               resume every parked session
  herdr-park panel                      interactive list (what the herdr popup runs)
  herdr-park version

TARGET is a pane id (w3:p14), a session name, or a session id (8+ chars).
Inside herdr it defaults to the focused pane. Add --dry-run to park or unpark
to print what would happen without changing anything.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-park:", sessions.Printable(err.Error()))
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	if cmd == "version" || cmd == "--version" {
		fmt.Println(version)
		return nil
	}
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		fmt.Print(usage)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config %s: %w", config.Path(), err)
	}
	client, err := herdr.New()
	if err != nil {
		return err
	}
	p := park.New(client, state.Store{Path: state.DefaultPath()}, cfg.ClaudeDir)
	notify := os.Getenv("HERDR_PLUGIN_ACTION_ID") != "" // headless plugin action: report via toast

	switch cmd {
	case "list":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "print JSON")
		if err := fs.Parse(args); err != nil {
			return err
		}
		rows, err := p.List()
		if err != nil {
			return err
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(rows)
		}
		printTable(rows, p.Now())
		return nil

	case "park", "unpark":
		fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
		force := fs.Bool("force", false, "park even if the agent is working")
		idle := fs.Bool("idle", false, "park every session idle for --days or more")
		days := fs.Int("days", cfg.ParkIdleDays, "idle threshold in days for --idle")
		all := fs.Bool("all", false, "resume every parked session")
		tab := fs.Bool("tab", false, "every session in TARGET's tab (default: the focused tab)")
		dry := fs.Bool("dry-run", false, "print what would happen and change nothing")
		pos, err := parseArgs(fs, args)
		if err != nil {
			return err
		}
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		switch {
		case cmd == "unpark" && (set["force"] || set["idle"] || set["days"]):
			return fmt.Errorf("usage: herdr-park unpark [--dry-run] [--all | --tab [TARGET] | TARGET]")
		case cmd == "park" && set["all"]:
			return fmt.Errorf("usage: herdr-park park [--force] [--tab] [TARGET] | park --idle [--days N]")
		case *idle && (len(pos) > 0 || *force || *tab || *days < 1):
			return fmt.Errorf("usage: herdr-park park --idle [--days N]  (N ≥ 1)")
		case set["days"] && !*idle:
			return fmt.Errorf("--days only applies with --idle")
		case *all && (len(pos) > 0 || *tab):
			return fmt.Errorf("usage: herdr-park unpark --all")
		case *tab && *force:
			return fmt.Errorf("--force parks one session at a time; leave out --tab")
		case len(pos) > 1:
			return fmt.Errorf("one session at a time; got %d: %s", len(pos), strings.Join(pos, " "))
		}
		rows, err := p.List()
		if err != nil {
			return err
		}
		var target string
		if len(pos) > 0 {
			target = pos[0]
		}
		if target == "" {
			target = focusedPane()
		}
		switch {
		case *idle:
			batch := park.Idle(rows, p.Now(), time.Duration(*days)*24*time.Hour, focusedPane())
			return parkBatch(client, p, batch, nil, fmt.Sprintf("No session has been idle for %dd or more.", *days), *dry, notify)
		case *all:
			return unparkBatch(client, p, park.Resumable(rows), "No parked sessions.", *dry, notify)
		case *tab:
			t, err := tabOf(client, rows, target)
			if err != nil {
				report(client, notify, "", "", err)
				return err
			}
			if cmd == "unpark" {
				return unparkBatch(client, p, park.ParkedInTab(rows, t), "No parked sessions in this tab.", *dry, notify)
			}
			// A plugin action means "this tab", focused pane included. From a
			// terminal, never park the session running this command.
			self := ""
			if !notify {
				self = os.Getenv("HERDR_PANE_ID")
			}
			ready, busy := park.InTab(rows, t, self)
			return parkBatch(client, p, ready, busy, "No idle session in this tab.", *dry, notify)
		}
		row, err := park.Find(rows, target)
		switch {
		case err != nil:
			report(client, notify, "", "", err)
		case *dry && cmd == "park":
			if err = park.CanPark(row, park.ParkOptions{Force: *force}); err == nil {
				fmt.Println("would park", describe(row, p.Now()))
			}
		case *dry:
			if err = park.CanUnpark(row); err == nil {
				fmt.Println("would resume", describe(row, p.Now()))
			}
		case cmd == "park":
			err = p.Park(row, park.ParkOptions{Force: *force})
			report(client, notify, "Parked "+row.Name, "Pane kept. Unpark to resume.", err)
		default:
			var pane string
			pane, err = p.Unpark(row)
			report(client, notify, "Resumed "+row.Name, "in "+pane, err)
		}
		return err

	case "hook":
		return runHook(cfg, client, p)

	case "panel":
		fs := flag.NewFlagSet("panel", flag.ContinueOnError)
		print := fs.Bool("print", false, "print one frame and exit")
		width := fs.Int("width", 80, "frame width with --print")
		height := fs.Int("height", 24, "frame height with --print")
		if err := fs.Parse(args); err != nil {
			return err
		}
		opt := tui.Options{
			Self:     focusedPane(),
			IdleDays: cfg.ParkIdleDays,
			Refresh:  time.Duration(cfg.RefreshSeconds) * time.Second,
		}
		if *print {
			frame, err := tui.Render(p, opt, *width, *height)
			fmt.Println(frame)
			return err
		}
		return tui.Run(p, opt)
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

// parseArgs parses flags placed before or after positional arguments
// (Go's flag package stops at the first positional one) and returns the
// positional arguments.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// focusedPane is the pane the user was in when invoking herdr-park: the
// plugin context's focused pane (inside a plugin pane, HERDR_PANE_ID is the
// plugin's own pane), else the calling pane.
func focusedPane() string {
	var ctx struct {
		FocusedPaneID string `json:"focused_pane_id"`
	}
	if raw := os.Getenv("HERDR_PLUGIN_CONTEXT_JSON"); raw != "" && json.Unmarshal([]byte(raw), &ctx) == nil && ctx.FocusedPaneID != "" {
		return ctx.FocusedPaneID
	}
	return os.Getenv("HERDR_PANE_ID")
}

// tabOf returns the tab of target: a session (see park.Find) or any pane.
func tabOf(c *herdr.Client, rows []sessions.Row, target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("no pane given; pass a pane id or a session name")
	}
	if r, err := park.Find(rows, target); err == nil && r.TabID != "" {
		return r.TabID, nil
	}
	pane, err := c.Pane(target)
	if err != nil {
		return "", fmt.Errorf("find the tab of %s: %w", target, err)
	}
	return pane.TabID, nil
}

// parkBatch parks batch, reporting busy sessions that were left running.
// With dry it only prints what it would do.
func parkBatch(c *herdr.Client, p *park.Parker, batch, busy []sessions.Row, none string, dry, notify bool) error {
	var left string
	if len(busy) > 0 {
		s := make([]string, len(busy))
		for i, r := range busy {
			s[i] = r.Name + " (" + r.Status + ")"
		}
		left = "Left running: " + strings.Join(s, ", ")
	}
	if dry {
		for _, r := range batch {
			fmt.Println("would park", describe(r, p.Now()))
		}
		for _, r := range busy {
			fmt.Println("would leave running", describe(r, p.Now()))
		}
		if len(batch) == 0 {
			fmt.Println(none)
		}
		return nil
	}
	if len(batch) == 0 {
		report(c, notify, "Nothing to park", strings.TrimSpace(none+" "+left), nil)
		return nil
	}
	n, err := p.ParkAll(batch)
	body := strings.Join(names(batch), ", ")
	if left != "" {
		body += ". " + left
	}
	report(c, notify, fmt.Sprintf("Parked %d sessions", n), body, err)
	return err
}

// unparkBatch resumes batch. With dry it only prints what it would do.
func unparkBatch(c *herdr.Client, p *park.Parker, batch []sessions.Row, none string, dry, notify bool) error {
	if dry {
		for _, r := range batch {
			fmt.Println("would resume", describe(r, p.Now()))
		}
		if len(batch) == 0 {
			fmt.Println(none)
		}
		return nil
	}
	if len(batch) == 0 {
		report(c, notify, "Nothing to resume", none, nil)
		return nil
	}
	n, err := p.UnparkAll(batch)
	report(c, notify, fmt.Sprintf("Resumed %d sessions", n), strings.Join(names(batch), ", "), err)
	return err
}

func names(rows []sessions.Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

// describe is one line about a session for --dry-run output.
func describe(r sessions.Row, now time.Time) string {
	where := "pane " + r.PaneID
	if r.Parked() && !r.PaneExists {
		where = "a new tab"
	}
	idle := ""
	if d := r.IdleFor(now); d > 0 {
		idle = ", idle " + sessions.FormatAge(d)
	}
	return fmt.Sprintf("%s (%s, %s%s, in %s)", sessions.Printable(r.Name), r.Kind, r.Status, idle, where)
}

func report(c *herdr.Client, notify bool, title, body string, err error) {
	if err != nil {
		title, body = "herdr-park", err.Error()
	} else {
		fmt.Println(sessions.Printable(title), sessions.Printable(body))
	}
	if notify {
		_ = c.Notify(title, body)
	}
}

func printTable(rows []sessions.Row, now time.Time) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tSTATUS\tIDLE\tRAM\tPANE\tSESSION ID\tDIR")
	home, _ := os.UserHomeDir()
	for _, r := range rows {
		idle := "-"
		if d := r.IdleFor(now); d > 0 {
			idle = sessions.FormatAge(d)
		}
		ram := "-"
		if r.RSSBytes > 0 {
			ram = sessions.FormatBytes(r.RSSBytes)
		} else if r.FreedBytes > 0 {
			ram = "(" + sessions.FormatBytes(r.FreedBytes) + ")" // freed by parking
		}
		sid := r.SessionID
		if len(sid) > 8 {
			sid = sid[:8]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", sessions.Printable(r.Name), r.Status, idle, ram, r.PaneID, sid,
			sessions.Printable(strings.Replace(r.Cwd, home, "~", 1)))
	}
	tw.Flush()
}
