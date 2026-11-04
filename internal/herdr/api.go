package herdr

import "time"

// AgentSession is the native session ref an agent integration reported.
type AgentSession struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Pane is the subset of herdr's pane/agent info that avocado-park uses.
// `agent list` and `pane list` share this shape; Agent is empty for plain shells.
type Pane struct {
	PaneID                string        `json:"pane_id"`
	TabID                 string        `json:"tab_id"`
	WorkspaceID           string        `json:"workspace_id"`
	Agent                 string        `json:"agent"`
	AgentStatus           string        `json:"agent_status"`
	AgentSession          *AgentSession `json:"agent_session"`
	Cwd                   string        `json:"cwd"`
	ForegroundCwd         string        `json:"foreground_cwd"`
	Focused               bool          `json:"focused"`
	Label                 string        `json:"label"`
	TerminalTitleStripped string        `json:"terminal_title_stripped"`
}

// SessionID returns the agent's native session id, if herdr knows it.
func (p Pane) SessionID() string {
	if p.AgentSession != nil && p.AgentSession.Kind == "id" {
		return p.AgentSession.Value
	}
	return ""
}

// Process is one foreground process of a pane.
type Process struct {
	PID   int    `json:"pid"`
	Argv0 string `json:"argv0"`
	Name  string `json:"name"`
	Cwd   string `json:"cwd"`
}

// Workspace is a herdr workspace.
type Workspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

// Agents lists every pane where herdr detected an agent.
func (c *Client) Agents() ([]Pane, error) {
	var r struct {
		Agents []Pane `json:"agents"`
	}
	return r.Agents, c.Call("agent.list", nil, &r)
}

// Panes lists every pane in every workspace.
func (c *Client) Panes() ([]Pane, error) {
	var r struct {
		Panes []Pane `json:"panes"`
	}
	return r.Panes, c.Call("pane.list", map[string]any{}, &r)
}

// Pane returns one pane.
func (c *Client) Pane(id string) (Pane, error) {
	var r struct {
		Pane Pane `json:"pane"`
	}
	return r.Pane, c.Call("pane.get", map[string]string{"pane_id": id}, &r)
}

// ForegroundProcesses returns the processes in a pane's foreground process group.
func (c *Client) ForegroundProcesses(pane string) ([]Process, error) {
	var r struct {
		ProcessInfo struct {
			Foreground []Process `json:"foreground_processes"`
		} `json:"process_info"`
	}
	return r.ProcessInfo.Foreground, c.Call("pane.process_info", map[string]string{"pane_id": pane}, &r)
}

// Workspaces lists workspaces.
func (c *Client) Workspaces() ([]Workspace, error) {
	var r struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	return r.Workspaces, c.Call("workspace.list", nil, &r)
}

// Prompt submits text plus Enter to an agent.
func (c *Client) Prompt(target, text string) error {
	return c.Call("agent.prompt", map[string]string{"target": target, "text": text}, nil)
}

// StartAgent starts an agent in a pane that sits at a shell prompt and waits
// until herdr detects it and it is ready for input.
func (c *Client) StartAgent(name, kind, pane string, args []string, timeout time.Duration) error {
	params := map[string]any{
		"name": name, "kind": kind, "pane_id": pane, "args": args,
		"timeout_ms": timeout.Milliseconds(),
	}
	return c.CallTimeout("agent.start", params, nil, timeout+5*time.Second)
}

// RunInPane types a command line into a pane and presses Enter.
func (c *Client) RunInPane(pane, line string) error {
	return c.Call("pane.send_input", map[string]any{"pane_id": pane, "text": line, "keys": []string{"enter"}}, nil)
}

// Notify shows a herdr toast.
func (c *Client) Notify(title, body string) error {
	return c.Call("notification.show", map[string]any{"title": title, "body": body, "sound": "none"}, nil)
}

// FocusPane focuses a pane by id (the CLI only exposes neighbor focus).
func (c *Client) FocusPane(pane string) error {
	return c.Call("pane.focus", map[string]string{"pane_id": pane}, nil)
}

// ClosePane closes a pane. Closing a tab's last pane closes the tab too.
func (c *Client) ClosePane(pane string) error {
	return c.Call("pane.close", map[string]string{"pane_id": pane}, nil)
}

// CreateTab opens a new tab and returns its root pane id.
func (c *Client) CreateTab(workspace, cwd, label string) (string, error) {
	params := map[string]any{"workspace_id": workspace, "cwd": cwd, "label": label, "focus": false}
	var r struct {
		RootPane Pane `json:"root_pane"`
	}
	return r.RootPane.PaneID, c.Call("tab.create", params, &r)
}
