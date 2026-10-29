package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ClaudeSession is the part of Claude Code's ~/.claude/sessions/<pid>.json
// that avocado-park reads. The format is internal to Claude Code, so every field
// is optional and the data only enriches what herdr reports.
type ClaudeSession struct {
	PID         int    `json:"pid"`
	SessionID   string `json:"sessionId"`
	Name        string `json:"name"`
	UpdatedAtMs int64  `json:"updatedAt"`
}

// UpdatedAt is the last activity time, or zero if unknown.
func (s ClaudeSession) UpdatedAt() time.Time {
	if s.UpdatedAtMs <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(s.UpdatedAtMs)
}

// LoadClaudeSessions reads every session file, keyed by pid. Unreadable or
// unexpected files are skipped.
func LoadClaudeSessions(claudeDir string) map[int]ClaudeSession {
	out := map[int]ClaudeSession{}
	if claudeDir == "" {
		return out
	}
	files, _ := filepath.Glob(filepath.Join(claudeDir, "sessions", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s ClaudeSession
		if json.Unmarshal(b, &s) == nil && s.PID > 0 {
			out[s.PID] = s
		}
	}
	return out
}
