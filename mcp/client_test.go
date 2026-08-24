package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServerSource is a minimal MCP server used to exercise the protocol end
// to end. Testing against a real subprocess is the point: the handshake, the
// framing and the process lifecycle are exactly what an in-process fake
// would fail to cover.
const fakeServerSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type req struct {
	ID     int64           ` + "`json:\"id\"`" + `
	Method string          ` + "`json:\"method\"`" + `
	Params json.RawMessage ` + "`json:\"params\"`" + `
}

func main() {
	mode := os.Getenv("FAKE_MODE")
	if mode == "crash" {
		os.Exit(1)
	}
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	sawInitialized := false

	for {
		line, err := in.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			return
		}
		var r req
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		switch r.Method {
		case "notifications/initialized":
			sawInitialized = true
			continue
		case "initialize":
			fmt.Fprintf(out, ` + "`" + `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"fake"}}}` + "`" + `+"\n", r.ID)
		case "tools/list":
			if !sawInitialized {
				fmt.Fprintf(out, ` + "`" + `{"jsonrpc":"2.0","id":%d,"error":{"code":-32002,"message":"not initialized"}}` + "`" + `+"\n", r.ID)
				break
			}
			fmt.Fprintf(out, ` + "`" + `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"query","description":"Run a query.","inputSchema":{"type":"object","properties":{"sql":{"type":"string"}},"required":["sql"]}},{"name":"noschema","description":""}]}}` + "`" + `+"\n", r.ID)
		case "tools/call":
			var p struct {
				Name      string         ` + "`json:\"name\"`" + `
				Arguments map[string]any ` + "`json:\"arguments\"`" + `
			}
			json.Unmarshal(r.Params, &p)
			switch p.Name {
			case "query":
				fmt.Fprintf(out, ` + "`" + `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"rows: %v"}]}}` + "`" + `+"\n", r.ID, p.Arguments["sql"])
			case "boom":
				fmt.Fprintf(out, ` + "`" + `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"table missing"}],"isError":true}}` + "`" + `+"\n", r.ID)
			case "picture":
				fmt.Fprintf(out, ` + "`" + `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"image","data":"..."}]}}` + "`" + `+"\n", r.ID)
			case "slow":
				time.Sleep(30 * time.Second)
			default:
				fmt.Fprintf(out, ` + "`" + `{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"no such tool"}}` + "`" + `+"\n", r.ID)
			}
		}
		out.Flush()
	}
}
`

var (
	buildOnce sync.Once
	serverBin string
	buildErr  error
)

func fakeServer(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mcp-fake")
		if err != nil {
			buildErr = err
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(fakeServerSource), 0o644); err != nil {
			buildErr = err
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fake\n\ngo 1.24\n"), 0o644); err != nil {
			buildErr = err
			return
		}
		serverBin = filepath.Join(dir, "fake")
		cmd := exec.Command("go", "build", "-o", serverBin, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = err
			serverBin = string(out)
		}
	})
	if buildErr != nil {
		t.Fatalf("build fake server: %v: %s", buildErr, serverBin)
	}
	return serverBin
}

func connect(t *testing.T, env map[string]string) *Client {
	t.Helper()
	c, err := Connect(t.Context(), ServerConfig{
		Name:    "fake",
		Command: fakeServer(t),
		Env:     env,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestConnectCompletesHandshake(t *testing.T) {
	c := connect(t, nil)
	if c.Name() != "fake" {
		t.Fatalf("Name = %q", c.Name())
	}
}

func TestListToolsReturnsAdaptedTools(t *testing.T) {
	c := connect(t, nil)
	tools, err := c.ListTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name() != "fake__query" {
		t.Fatalf("Name = %q, want the server name prefixed", tools[0].Name())
	}
	if !strings.Contains(tools[0].Description(), "Run a query.") {
		t.Fatalf("Description lost the server's text: %q", tools[0].Description())
	}
	if !strings.Contains(tools[0].Description(), "MCP server") {
		t.Fatal("the description does not say where the tool came from")
	}
}

func TestToolsListRequiresInitializedNotification(t *testing.T) {
	// The fake refuses tools/list until it has seen the notification, so a
	// successful listing proves the client sent it.
	c := connect(t, nil)
	if _, err := c.ListTools(t.Context()); err != nil {
		t.Fatalf("tools/list failed, so the initialized notification was not sent: %v", err)
	}
}

func TestSchemaIsPassedThroughUnchanged(t *testing.T) {
	c := connect(t, nil)
	tools, err := c.ListTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(tools[0].InputSchema())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	props, ok := got["properties"].(map[string]any)
	if !ok || props["sql"] == nil {
		t.Fatalf("the server's schema was not preserved: %s", raw)
	}
	if got["required"] == nil {
		t.Fatal("the required list was dropped; only the server knows what its tool needs")
	}
}

func TestToolWithoutSchemaGetsAnEmptyObject(t *testing.T) {
	c := connect(t, nil)
	tools, _ := c.ListTools(t.Context())
	raw, err := json.Marshal(tools[1].InputSchema())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "object") {
		t.Fatalf("a tool with no schema produced %s", raw)
	}
}

func TestExecuteReturnsServerText(t *testing.T) {
	c := connect(t, nil)
	tools, _ := c.ListTools(t.Context())

	res, err := tools[0].Execute(t.Context(), json.RawMessage(`{"sql":"select 1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "select 1") {
		t.Fatalf("Content = %q, want the arguments to have reached the server", res.Content)
	}
	if res.Metadata["server"] != "fake" {
		t.Fatal("the result does not record which server answered")
	}
}

func TestExecuteReportsToolErrorAsResult(t *testing.T) {
	c := connect(t, nil)
	tool := &remoteTool{client: c, descriptor: toolDescriptor{Name: "boom"}}

	res, err := tool.Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("a server-side tool error ended the run: %v", err)
	}
	if !strings.HasPrefix(res.Content, "error:") {
		t.Fatalf("Content = %q, want it marked as an error", res.Content)
	}
	if res.Metadata["ok"] != false {
		t.Fatal("metadata does not record the failure")
	}
}

func TestExecuteReportsProtocolErrorAsResult(t *testing.T) {
	c := connect(t, nil)
	tool := &remoteTool{client: c, descriptor: toolDescriptor{Name: "absent"}}

	res, err := tool.Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("an unknown tool ended the run: %v", err)
	}
	if !strings.Contains(res.Content, "no such tool") {
		t.Fatalf("Content = %q, want the server's explanation", res.Content)
	}
}

func TestExecuteDescribesNonTextContent(t *testing.T) {
	c := connect(t, nil)
	tool := &remoteTool{client: c, descriptor: toolDescriptor{Name: "picture"}}

	res, err := tool.Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "image") {
		t.Fatalf("Content = %q, want it to say what was omitted rather than return nothing", res.Content)
	}
}

func TestRequestTimesOut(t *testing.T) {
	c, err := Connect(t.Context(), ServerConfig{
		Name:    "fake",
		Command: fakeServer(t),
		Timeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tool := &remoteTool{client: c, descriptor: toolDescriptor{Name: "slow"}}
	started := time.Now()
	res, err := tool.Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Fatalf("Content = %q, want a timeout notice", res.Content)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the timeout took %s to fire", elapsed)
	}
}

func TestConnectFailsOnServerThatExits(t *testing.T) {
	_, err := Connect(t.Context(), ServerConfig{
		Name:    "crasher",
		Command: fakeServer(t),
		Env:     map[string]string{"FAKE_MODE": "crash"},
		Timeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("connecting to a server that exits immediately succeeded")
	}
}

func TestConnectRejectsMissingCommand(t *testing.T) {
	if _, err := Connect(context.Background(), ServerConfig{Name: "x"}); err == nil {
		t.Fatal("a server with no command was accepted")
	}
}

func TestConnectReportsUnlaunchableCommand(t *testing.T) {
	_, err := Connect(context.Background(), ServerConfig{
		Name:    "x",
		Command: "/nonexistent/mcp-server",
		Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("a command that cannot be launched was accepted")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	c := connect(t, nil)
	c.Close()
	c.Close()
}
