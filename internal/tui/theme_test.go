package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestThemeIDMatchesAvocadoTrope(t *testing.T) {
	for in, want := range map[string]string{
		"dracula": "dracula", "Tokyo_Night_Storm": "tokyo-night", "catppuccin-mocha": "catppuccin",
		"catppuccin-latte": "terminal", "gruvbox-dark": "gruvbox", "gruvbox-light": "terminal",
		"nord": "nord", "zoetrope": "classic", "default": "classic", "bogus": "", "": "",
	} {
		if got := themeID(in); got != want {
			t.Errorf("themeID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPickThemeOrder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HERDR_CONFIG_PATH", filepath.Join(dir, "herdr.toml"))
	t.Setenv("AVOCADO_PARK_THEME", "")
	t.Setenv("AVOCADO_THEME", "")
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string) {
		t.Helper()
		if got := PickTheme(); got != want {
			t.Errorf("PickTheme() = %q, want %q", got, want)
		}
	}

	check("dracula")
	write(filepath.Join(dir, "herdr.toml"), "[ui]\nname = \"x\"\n[theme]\nname_override = 1\nname = \"nord\" # comment\n")
	check("nord")
	write(filepath.Join(dir, "avocado-trope", "theme"), "gruvbox\n")
	check("gruvbox")
	t.Setenv("AVOCADO_THEME", "catppuccin")
	check("catppuccin")
	t.Setenv("AVOCADO_PARK_THEME", "tokyo-night")
	check("tokyo-night")
	t.Setenv("AVOCADO_PARK_THEME", "not-a-theme") // ignored, falls through
	check("catppuccin")
}

func TestPaintKeepsForegroundsAndFillsWidth(t *testing.T) {
	r := lipgloss.DefaultRenderer()
	old := r.ColorProfile()
	r.SetColorProfile(termenv.TrueColor)
	defer r.SetColorProfile(old)

	bg := lipgloss.Color("#282a36")
	out := paint(lipgloss.NewStyle().Foreground(lipgloss.Color("#ff79c6")).Render("hi")+" there", bg, 12)
	seq := bgSeq(bg)
	if seq == "" {
		t.Fatal("no background sequence with a true-color profile")
	}
	if !strings.HasPrefix(out, seq) || !strings.Contains(out, "\x1b[0m"+seq) {
		t.Errorf("background not restored after a styled span: %q", out)
	}
	if w := lipgloss.Width(out); w != 12 {
		t.Errorf("width %d, want 12", w)
	}
	if got := paint("plain", lipgloss.NoColor{}, 10); got != "plain" {
		t.Errorf("no background should leave the text alone, got %q", got)
	}
}
