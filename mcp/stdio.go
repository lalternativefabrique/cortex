package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// stdioTransport is JSON-RPC 2.0 over a subprocess's stdin/stdout,
// newline-delimited.
type stdioTransport struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	timeout time.Duration

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response

	closeOnce sync.Once
	closed    chan struct{}
	readErr   error
}

func newStdioTransport(sc ServerConfig) (*stdioTransport, error) {
	cmd := exec.Command(sc.Command, sc.Args...)
	cmd.Env = os.Environ()
	for k, v := range sc.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// A server that fails to start explains itself there, so the stream is
	// forwarded rather than discarded.
	if sc.Stderr != nil {
		cmd.Stderr = sc.Stderr
	} else {
		cmd.Stderr = os.Stderr
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: stdin: %w", sc.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: stdout: %w", sc.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %s: start %q: %w", sc.Name, sc.Command, err)
	}

	t := &stdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, 1024*1024),
		timeout: sc.Timeout,
		pending: map[int64]chan response{},
		closed:  make(chan struct{}),
	}
	go t.readLoop()
	return t, nil
}

// readLoop dispatches responses to whoever is waiting for that id.
func (t *stdioTransport) readLoop() {
	defer close(t.closed)
	for {
		line, err := t.stdout.ReadBytes('\n')
		if len(line) > 0 {
			var resp response
			if err := json.Unmarshal(line, &resp); err == nil && resp.ID != 0 {
				t.deliver(resp)
			}
			// A line that is not a response to something we asked is a
			// notification or a log; the client has no use for either.
		}
		if err != nil {
			t.mu.Lock()
			t.readErr = err
			for id, ch := range t.pending {
				close(ch)
				delete(t.pending, id)
			}
			t.mu.Unlock()
			return
		}
	}
}

func (t *stdioTransport) deliver(resp response) {
	t.mu.Lock()
	ch, ok := t.pending[resp.ID]
	if ok {
		delete(t.pending, resp.ID)
	}
	t.mu.Unlock()
	if ok {
		ch <- resp
		close(ch)
	}
}

func (t *stdioTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	if t.readErr != nil {
		t.mu.Unlock()
		return nil, fmt.Errorf("connection closed: %w", t.readErr)
	}
	t.nextID++
	id := t.nextID
	ch := make(chan response, 1)
	t.pending[id] = ch
	t.mu.Unlock()

	if err := t.write(request{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, fmt.Errorf("%s timed out", method)
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("server exited during %s", method)
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (t *stdioTransport) notify(_ context.Context, method string, params any) error {
	return t.write(request{JSONRPC: "2.0", Method: method, Params: params})
}

func (t *stdioTransport) write(r request) error {
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode %s: %w", r.Method, err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", r.Method, err)
	}
	return nil
}

func (t *stdioTransport) close() error {
	t.closeOnce.Do(func() {
		t.stdin.Close()
		// Give the server a moment to exit on its closed stdin before
		// killing it, so it can flush and clean up.
		done := make(chan struct{})
		go func() {
			t.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if t.cmd.Process != nil {
				t.cmd.Process.Kill()
			}
			<-done
		}
	})
	return nil
}
