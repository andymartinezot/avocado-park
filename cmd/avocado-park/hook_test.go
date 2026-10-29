package main

import "testing"

func TestEventPane(t *testing.T) {
	cases := map[string]string{
		`{"data":{"pane_id":"w2:p1B","type":"pane_focused","workspace_id":"w2"},"event":"pane_focused"}`: "w2:p1B", // as herdr sends it
		`{"pane_id":"w1:p2"}`:                                     "w1:p2",
		`{"type":"pane.focused","data":{"pane_id":"w1:p3"}}`:      "w1:p3",
		`{"event":{"pane":{"pane_id":"w1:p4","tab_id":"w1:t1"}}}`: "w1:p4",
		`{"pane_id":"w1:p5","previous":{"pane_id":"w1:p1"}}`:      "w1:p5",
		`{"workspace_id":"w1"}`:                                   "",
		`not json`:                                                "",
		``:                                                        "",
	}
	for raw, want := range cases {
		if got := eventPane(raw); got != want {
			t.Errorf("eventPane(%q) = %q, want %q", raw, got, want)
		}
	}
}
