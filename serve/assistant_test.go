package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/mcp"
)

func TestAnAssistantGetsLightFileToolsAndNoShell(t *testing.T) {
	s, err := New(Config{Root: t.TempDir(), Instructions: "You help a person with their day."})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range s.tools {
		got[tool.Name()] = true
	}
	for _, want := range []string{"read", "grep", "glob", "write"} {
		if !got[want] {
			t.Errorf("%s is missing", want)
		}
	}
	for _, refused := range []string{"edit", "bash"} {
		if got[refused] {
			t.Errorf("an assistant was given %s", refused)
		}
	}
}

func TestAnAssistantIsPromptedByItsInstructionsAndItsServers(t *testing.T) {
	lalter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"protocolVersion":"2024-11-05","instructions":"Draft a mail with messag_draft."}}`))
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"tools":[]}}`))
		}
	}))
	defer lalter.Close()

	s, err := New(Config{
		Root:         t.TempDir(),
		Instructions: "You help a person with their day.",
		MCP:          mcp.Config{Servers: []mcp.ServerConfig{{Name: "lalter", URL: lalter.URL}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	prompt := s.system("", false)
	if !strings.HasPrefix(prompt, "You help a person with their day.") {
		t.Fatalf("the assistant's instructions do not lead the prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Draft a mail with messag_draft.") {
		t.Fatal("the server's instructions were dropped")
	}
	if strings.Contains(prompt, "coding agent") {
		t.Fatal("an assistant was told it is a coding agent")
	}
}

func TestAnAssistantReadsNoHiddenFile(t *testing.T) {
	files := t.TempDir()
	if err := os.MkdirAll(filepath.Join(files, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(files, ".ssh", "id_ed25519"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(files, "bulletin.txt"), []byte("salaire"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Root: t.TempDir(), Files: files, Instructions: "You help a person with their day."})
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		for _, tool := range s.tools {
			if tool.Name() == "read" {
				res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
				if err != nil {
					t.Fatal(err)
				}
				return res.Content
			}
		}
		t.Fatal("no read tool")
		return ""
	}
	if got := read(".ssh/id_ed25519"); strings.Contains(got, "secret") {
		t.Fatal("a hidden file was read")
	}
	if got := read("bulletin.txt"); !strings.Contains(got, "salaire") {
		t.Fatalf("a plain file was not read: %s", got)
	}
}

func TestAHostToolIsOffered(t *testing.T) {
	s, err := New(Config{Root: t.TempDir(), Instructions: "x", Tools: []agent.Tool{newReadAttachment(nil, []Attachment{{Name: "a"}})}})
	if err != nil {
		t.Fatal(err)
	}
	if !named(s.tools, readAttachmentName) {
		t.Errorf("tools %v", toolNames(s.tools))
	}
}
