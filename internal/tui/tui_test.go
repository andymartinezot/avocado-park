package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andymartinezot/avocado-park/internal/sessions"
)

func rows(names ...string) []sessions.Row {
	out := make([]sessions.Row, len(names))
	for i, n := range names {
		out[i] = sessions.Row{Name: n, SessionID: "id-" + n, Workspace: "ws-" + n}
	}
	return out
}

func selected(m model) string {
	if m.cur >= len(m.rows) {
		return ""
	}
	return m.rows[m.cur].Name
}

// A refresh reorders the list; the cursor must stay on the same session so
// that p/u never act on a different one than the user picked.
func TestCursorFollowsSessionAcrossRefresh(t *testing.T) {
	var m model
	m.setRows(rows("a", "b", "c"))
	m.cur = 1 // b
	m.setRows(rows("c", "a", "b"))
	if got := selected(m); got != "b" {
		t.Fatalf("selected %q after reorder, want b", got)
	}
	m.setRows(rows("c", "a")) // b went away
	if m.cur >= len(m.rows) {
		t.Fatalf("cursor %d out of range", m.cur)
	}
}

func TestFilter(t *testing.T) {
	var m model
	m.setRows(rows("deploy", "dataset", "datalake"))
	press := func(s string) {
		for _, r := range s {
			mm, _ := m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = mm.(model)
		}
	}
	press("/DATA")
	if len(m.rows) != 2 || !m.filtering {
		t.Fatalf("rows %v filtering %v", m.rows, m.filtering)
	}
	// Workspace matches too.
	mm, _ := m.key(tea.KeyMsg{Type: tea.KeyBackspace})
	m = mm.(model)
	if m.filter != "DAT" {
		t.Fatalf("filter %q", m.filter)
	}
	mm, _ = m.key(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(model)
	if m.filtering || len(m.rows) != 2 {
		t.Fatalf("after enter: filtering %v rows %d", m.filtering, len(m.rows))
	}
	// A refresh keeps the filter.
	m.setRows(rows("dataset", "deploy", "datalake", "new"))
	if len(m.rows) != 2 {
		t.Fatalf("refresh dropped filter: %d rows", len(m.rows))
	}
	// Esc clears the filter instead of quitting.
	mm, cmd := m.key(tea.KeyMsg{Type: tea.KeyEsc})
	m = mm.(model)
	if cmd != nil || m.filter != "" || len(m.rows) != 4 {
		t.Fatalf("esc: cmd %v filter %q rows %d", cmd != nil, m.filter, len(m.rows))
	}
	m.setRows(rows("x"))
	press("/ws-x")
	if len(m.rows) != 1 {
		t.Fatalf("workspace match: %d rows", len(m.rows))
	}
}
