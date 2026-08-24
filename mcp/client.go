// Package mcp speaks the Model Context Protocol, so tools provided by
// external servers — a database, an issue tracker, a browser — can be handed
// to the agent alongside the built-in ones.
//
// MCP is JSON-RPC 2.0 over stdio: the client spawns the server as a
// subprocess and exchanges newline-delimited JSON on its pipes.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// ProtocolVersion is the revision this client implements. A server that
// answers with a different one still works for the tools/* calls used here;
// the field is sent because the handshake requires it.
const ProtocolVersion = "2024-11-05"

// ServerConfig describes how to launch one MCP server.
type ServerConfig struct {
	Name    string            `yaml:"name" json:"name"`
	Command string            `yaml:"command" json:"command"`
	Args    []string          `yaml:"args" json:"args"`
	Env     map[string]string `yaml:"env" json:"env"`
	// Timeout caps a single request. Zero means DefaultRequestTimeout.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
	// Stderr receives the server's diagnostics. Nil sends them to the
	// process's own stderr, which suits a CLI; a service handling several
	// runs at once wants them separated per run.
	Stderr io.Writer `yaml:"-" json:"-"`
}

const DefaultRequestTimeout = 60 * time.Second

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// Client is a connection to one MCP server.
type Client struct {
	cfg    ServerConfig
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response

	closeOnce sync.Once
	closed    chan struct{}
	readErr   error
}

// Connect launches the server and completes the initialize handshake.
func Connect(ctx context.Context, cfg ServerConfig) (*Client, error) {
	if cfg.Command == "" {
		return nil, errors.New("mcp: Command is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultRequestTimeout
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// A server that fails to start explains itself there, so the stream is
	// forwarded rather than discarded.
	if cfg.Stderr != nil {
		cmd.Stderr = cfg.Stderr
	} else {
		cmd.Stderr = os.Stderr
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: stdin: %w", cfg.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: stdout: %w", cfg.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %s: start %q: %w", cfg.Name, cfg.Command, err)
	}

	c := &Client{
		cfg:     cfg,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, 1024*1024),
		pending: map[int64]chan response{},
		closed:  make(chan struct{}),
	}
	go c.readLoop()

	if err := c.handshake(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) handshake(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "skode", "version": "0.1.0"},
	})
	if err != nil {
		return fmt.Errorf("mcp %s: initialize: %w", c.cfg.Name, err)
	}
	// The spec requires this notification before any other request; servers
	// that enforce it reject tools/list without it.
	return c.notify("notifications/initialized", map[string]any{})
}

// readLoop dispatches responses to whoever is waiting for that id.
func (c *Client) readLoop() {
	defer close(c.closed)
	for {
		line, err := c.stdout.ReadBytes('\n')
		if len(line) > 0 {
			var resp response
			if err := json.Unmarshal(line, &resp); err == nil && resp.ID != 0 {
				c.deliver(resp)
			}
			// A line that is not a response to something we asked is a
			// notification or a log; the client has no use for either.
		}
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
	}
}

func (c *Client) deliver(resp response) {
	c.mu.Lock()
	ch, ok := c.pending[resp.ID]
	if ok {
		delete(c.pending, resp.ID)
	}
	c.mu.Unlock()
	if ok {
		ch <- resp
		close(ch)
	}
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	cctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	c.mu.Lock()
	if c.readErr != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp %s: connection closed: %w", c.cfg.Name, c.readErr)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(request{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-cctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp %s: %s timed out after %s", c.cfg.Name, method, c.cfg.Timeout)
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("mcp %s: server exited during %s", c.cfg.Name, method)
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// notify sends a request that expects no reply.
func (c *Client) notify(method string, params any) error {
	return c.write(request{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Client) write(r request) error {
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("mcp %s: encode %s: %w", c.cfg.Name, r.Method, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("mcp %s: write %s: %w", c.cfg.Name, r.Method, err)
	}
	return nil
}

// Close shuts the server down.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.stdin.Close()
		// Give the server a moment to exit on its closed stdin before
		// killing it, so it can flush and clean up.
		done := make(chan struct{})
		go func() {
			c.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if c.cmd.Process != nil {
				c.cmd.Process.Kill()
			}
			<-done
		}
	})
	return nil
}

// Name is the server's configured name.
func (c *Client) Name() string { return c.cfg.Name }
