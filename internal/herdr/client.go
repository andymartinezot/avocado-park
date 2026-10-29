// Package herdr is a small client for herdr's local socket API.
//
// Every request opens its own connection and sends one newline-delimited JSON
// object, which is how herdr's own CLI talks to the server.
package herdr

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Client talks to a running herdr server.
type Client struct {
	Socket  string
	Timeout time.Duration
}

// Error is an error returned by the herdr server.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// IsCode reports whether err is a herdr server error with the given code.
func IsCode(err error, code string) bool {
	var he *Error
	return errors.As(err, &he) && he.Code == code
}

// SocketPath returns the socket herdr injects into plugins and panes, or the
// default location when running outside herdr (for example from a menu bar app).
func SocketPath() string {
	if s := os.Getenv("HERDR_SOCKET_PATH"); s != "" {
		return s
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr", "herdr.sock")
}

// New returns a client for the herdr socket, failing early if herdr is not running.
func New() (*Client, error) {
	s := SocketPath()
	if _, err := os.Stat(s); err != nil {
		return nil, fmt.Errorf("herdr socket not found at %s: is herdr running?", s)
	}
	return &Client{Socket: s, Timeout: 10 * time.Second}, nil
}

// Call sends one request and decodes its result into out (which may be nil).
func (c *Client) Call(method string, params, out any) error {
	return c.CallTimeout(method, params, out, c.Timeout)
}

// CallTimeout is Call with an explicit deadline, for requests that wait on agents.
func (c *Client) CallTimeout(method string, params, out any, timeout time.Duration) error {
	conn, err := net.DialTimeout("unix", c.Socket, 2*time.Second)
	if err != nil {
		return fmt.Errorf("connect to herdr: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	if params == nil {
		params = struct{}{}
	}
	req, err := json.Marshal(map[string]any{"id": "avocado-park:" + method, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}

	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return fmt.Errorf("%s: read response: %w", method, err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}
