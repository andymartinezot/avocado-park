// Command fakeagent stands in for an agent in the end-to-end test. Built under
// the agent's name, it does what avocado-park relies on: it writes a session
// file, tells herdr its session id and state, resumes the session it is given
// and quits on /exit.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	id := "e2e-session-0001"
	if len(os.Args) == 3 && os.Args[1] == "--resume" {
		id = os.Args[2]
	}
	logf, err := os.OpenFile(os.Getenv("FAKE_AGENT_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer logf.Close()
	fmt.Fprintln(logf, strings.Join(os.Args[1:], " "))

	pid := os.Getpid()
	file := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "sessions", strconv.Itoa(pid)+".json")
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": id, "name": "e2e-agent", "updatedAt": time.Now().UnixMilli()})
	if err := os.WriteFile(file, b, 0o600); err != nil {
		panic(err)
	}
	defer os.Remove(file)

	report := exec.Command("herdr", "pane", "report-agent", "--source", "e2e", "--agent", "claude", "--state", "idle",
		"--agent-session-id", id, os.Getenv("HERDR_PANE_ID"))
	if out, err := report.CombinedOutput(); err != nil {
		fmt.Fprintf(logf, "report-agent: %v: %s\n", err, out)
	}

	fmt.Println("e2e agent ready")
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		if strings.TrimSpace(in.Text()) == "/exit" {
			return
		}
	}
}
