package sessions

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Printable replaces control characters (C0, DEL, C1, which include ESC and
// CSI) and invalid UTF-8 with "?". Names come from terminal titles that any
// program can set, so they are cleaned before reaching a terminal.
func Printable(s string) string {
	if utf8.ValidString(s) && !strings.ContainsFunc(s, isControl) {
		return s
	}
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "?") {
		if isControl(r) {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isControl(r rune) bool { return r < 0x20 || (r >= 0x7f && r <= 0x9f) }

// FormatAge renders a duration compactly: now, 12m, 5h, 9d.
func FormatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// FormatBytes renders a size compactly: 512K, 291M, 1.2G.
func FormatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%dM", n>>20)
	default:
		return fmt.Sprintf("%dK", n>>10)
	}
}
