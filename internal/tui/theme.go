package tui

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/andymartinezot/avocado-park/internal/sessions"
)

// The avocado palette, shared by every avocado herdr plugin: the same theme
// ids, colors and roles as avocado-trope (src/theme.rs there) and
// avocado-keep-awake, so the plugins look like one family.
type palette struct {
	canvas, surface, muted, subtle, accent, text, success, errc lipgloss.TerminalColor
	// Roles: running is a working agent, warn needs attention, group is the
	// purple used for "done", parked uses the subagent blue.
	running, warn, group, parked lipgloss.TerminalColor
}

type look struct {
	id string
	p  palette
}

var curID string

func hex(s string) lipgloss.Color { return lipgloss.Color(s) }

var none = lipgloss.NoColor{}

var looks = []look{
	{"dracula", palette{hex("#21222c"), hex("#282a36"), hex("#44475a"), hex("#6272a4"), hex("#ff79c6"), hex("#f8f8f2"), hex("#50fa7b"), hex("#ff5555"),
		hex("#50fa7b"), hex("#f1fa8c"), hex("#bd93f9"), hex("#8be9fd")}},
	{"catppuccin", palette{hex("#181825"), hex("#1e1e2e"), hex("#45475a"), hex("#7f849c"), hex("#f5c2e7"), hex("#cdd6f4"), hex("#a6e3a1"), hex("#f38ba8"),
		hex("#a6e3a1"), hex("#f9e2af"), hex("#cba6f7"), hex("#89b4fa")}},
	{"tokyo-night", palette{hex("#16161e"), hex("#1a1b26"), hex("#3b4261"), hex("#565f89"), hex("#e0af68"), hex("#c0caf5"), hex("#9ece6a"), hex("#f7768e"),
		hex("#9ece6a"), hex("#e0af68"), hex("#bb9af7"), hex("#7aa2f7")}},
	{"gruvbox", palette{hex("#1d2021"), hex("#282828"), hex("#504945"), hex("#928374"), hex("#fabd2f"), hex("#ebdbb2"), hex("#b8bb26"), hex("#fb4934"),
		hex("#b8bb26"), hex("#fabd2f"), hex("#d3869b"), hex("#83a598")}},
	{"nord", palette{hex("#242933"), hex("#2e3440"), hex("#434c5e"), hex("#616e88"), hex("#ebcb8b"), hex("#eceff4"), hex("#a3be8c"), hex("#bf616a"),
		hex("#a3be8c"), hex("#ebcb8b"), hex("#b48ead"), hex("#88c0d0")}},
	// 256-color indexes, like rataflow's dark palette with a gold accent.
	{"classic", palette{hex("233"), hex("234"), hex("237"), hex("240"), hex("178"), hex("231"), hex("71"), hex("167"),
		hex("71"), hex("214"), hex("178"), hex("240")}},
	// The terminal's own ANSI colors on its own background: right for light terminals.
	{"terminal", palette{none, none, hex("8"), hex("8"), hex("4"), none, hex("2"), hex("1"),
		hex("2"), hex("3"), hex("5"), hex("6")}},
}

// ThemeNames lists the theme ids, the first being the default.
func ThemeNames() []string {
	names := make([]string, len(looks))
	for i, l := range looks {
		names[i] = l.id
	}
	return names
}

// themeID maps a theme name to an id, the way avocado-trope does: herdr's
// tokyo-night-storm, catppuccin-mocha, gruvbox-dark… work, and light variants
// (latte, day, light, dawn) use terminal. Unknown names return "".
func themeID(name string) string {
	n := strings.NewReplacer("_", "-", " ", "-").Replace(strings.ToLower(strings.TrimSpace(name)))
	parts := strings.Split(n, "-")
	switch {
	case slices.ContainsFunc(parts, func(p string) bool { return p == "latte" || p == "day" || p == "light" || p == "dawn" }):
		n = "terminal"
	case strings.HasPrefix(n, "tokyo"):
		n = "tokyo-night"
	case strings.HasPrefix(n, "catppuccin"):
		n = "catppuccin"
	case strings.HasPrefix(n, "gruvbox"):
		n = "gruvbox"
	case n == "zoetrope" || n == "default":
		n = "classic"
	}
	if slices.Contains(ThemeNames(), n) {
		return n
	}
	return ""
}

// PickTheme returns the theme to use, the first of: $AVOCADO_PARK_THEME or
// $AVOCADO_THEME, the theme last picked in avocado-trope, herdr's
// [theme] name, then dracula.
func PickTheme() string {
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		if home, err := os.UserHomeDir(); err == nil {
			config = filepath.Join(home, ".config")
		}
	}
	herdrConf := os.Getenv("HERDR_CONFIG_PATH")
	if herdrConf == "" && config != "" {
		herdrConf = filepath.Join(config, "herdr", "config.toml")
	}
	candidates := []func() string{
		func() string { return os.Getenv("AVOCADO_PARK_THEME") },
		func() string { return os.Getenv("AVOCADO_THEME") },
		func() string { return firstLine(filepath.Join(config, "avocado-trope", "theme")) },
		func() string { return herdrTheme(herdrConf) },
	}
	for _, c := range candidates {
		if id := themeID(c()); id != "" {
			return id
		}
	}
	return looks[0].id
}

func firstLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return line
}

// herdrTheme reads `name` under [theme] in herdr's config.toml. A line scan:
// the file may hold keys this program has no reason to parse.
func herdrTheme(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = line == "[theme]"
			continue
		}
		rest, ok := strings.CutPrefix(line, "name")
		if !in || !ok {
			continue
		}
		v, ok := strings.CutPrefix(strings.TrimSpace(rest), "=")
		if !ok {
			continue
		}
		v, _, _ = strings.Cut(v, "#")
		return strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return ""
}

// Styles, set by applyTheme.
var (
	cur       palette
	titleSt   lipgloss.Style
	headSt    lipgloss.Style
	dimSt     lipgloss.Style
	selSt     lipgloss.Style
	keySt     lipgloss.Style
	errSt     lipgloss.Style
	okSt      lipgloss.Style
	staleSt   lipgloss.Style
	boxSt     lipgloss.Style
	statusSty map[string]lipgloss.Style
)

func init() { applyTheme(looks[0].id) }

// applyTheme switches every style to the theme id ("" picks with PickTheme).
func applyTheme(id string) {
	if id = themeID(id); id == "" {
		id = PickTheme()
	}
	i := slices.IndexFunc(looks, func(l look) bool { return l.id == id })
	p := looks[max(i, 0)].p
	cur, curID = p, id
	fg := func(c lipgloss.TerminalColor) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	// The title is a tag in the accent color, like avocado-trope's header.
	titleSt = lipgloss.NewStyle().Bold(true).Foreground(p.canvas).Background(p.accent).Padding(0, 1)
	headSt = fg(p.subtle).Bold(true)
	dimSt = fg(p.subtle)
	selSt = fg(p.accent).Bold(true)
	keySt = fg(p.accent).Bold(true)
	errSt = fg(p.errc)
	okSt = fg(p.success)
	staleSt = fg(p.group)
	boxSt = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(p.muted).Padding(0, 1)
	statusSty = map[string]lipgloss.Style{
		sessions.StatusWorking: fg(p.running),
		sessions.StatusBlocked: fg(p.warn),
		sessions.StatusParked:  fg(p.parked),
	}
}

// seqOf is the escape sequence that starts st, or "" when the terminal has
// no colors (or st sets nothing).
func seqOf(st lipgloss.Style) string {
	before, _, _ := strings.Cut(st.Render("x"), "x")
	return before
}

func bgSeq(c lipgloss.TerminalColor) string { return seqOf(lipgloss.NewStyle().Background(c)) }

// selectSeq marks the selected row: the muted background, as in
// avocado-trope, or reverse video in the terminal theme, where ANSI blue on
// gray is hard to read.
func selectSeq() string {
	if curID == "terminal" {
		return seqOf(lipgloss.NewStyle().Reverse(true))
	}
	return bgSeq(cur.muted)
}

// paint gives every cell of s the background c. See paintSeq.
func paint(s string, c lipgloss.TerminalColor, width int) string { return paintSeq(s, bgSeq(c), width) }

// paintSeq applies seq (a background) to every cell of s, keeping each span's
// own foreground: styled spans end in a full reset, which would drop it.
// Lines are padded to width.
func paintSeq(s, seq string, width int) string {
	if seq == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.ReplaceAll(l, "\x1b[0m", "\x1b[0m"+seq)
		lines[i] = seq + l + strings.Repeat(" ", max(0, width-lipgloss.Width(l))) + "\x1b[0m"
	}
	return strings.Join(lines, "\n")
}
