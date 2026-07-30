package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
)

// Client speaks herdr's newline-delimited JSON protocol over its unix socket.
// Most of herdr is reachable through the CLI, but layout.export and layout.apply
// are not, and those are the whole point of this plugin.
type Client struct {
	path string
	seq  atomic.Uint64
}

func NewClient() *Client {
	path := os.Getenv("HERDR_SOCKET_PATH")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".config", "herdr", "herdr.sock")
	}
	return &Client{path: path}
}

type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Call sends one request and decodes the result into out.
func (c *Client) Call(method string, params any, out any) error {
	conn, err := net.Dial("unix", c.path)
	if err != nil {
		return fmt.Errorf("herdr socket at %s: %w", c.path, err)
	}
	defer conn.Close()

	if params == nil {
		params = map[string]any{}
	}
	req, err := json.Marshal(map[string]any{
		"id":     fmt.Sprintf("resurrect-%d", c.seq.Add(1)),
		"method": method,
		"params": params,
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(conn, "%s\n", req); err != nil {
		return err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}

	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("%s: bad response: %w", method, err)
	}
	if resp.Error != nil {
		return fmt.Errorf("%s: %s (%s)", method, resp.Error.Message, resp.Error.Code)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}
