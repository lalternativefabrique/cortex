package serve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lalternativefabrique/cortex/agent"
)

func toolNamed(t *testing.T, all []agent.Tool, name string) agent.Tool {
	t.Helper()
	for _, tool := range all {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("no tool %s", name)
	return nil
}

func TestAReadRootIsReadButNeverWritten(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	file := filepath.Join(other, "notes.md")
	if err := os.WriteFile(file, []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	all := codingTools(Config{Root: root, ReadRoots: []string{other}})
	read, err := toolNamed(t, all, "read").Execute(context.Background(), json.RawMessage(`{"path":"`+file+`"}`))
	if err != nil || !strings.Contains(read.Content, "kept") {
		t.Fatalf("read: %+v, %v", read, err)
	}
	args, _ := json.Marshal(map[string]string{"path": filepath.Join(other, "new.md"), "content": "x"})
	if res, err := toolNamed(t, all, "write").Execute(context.Background(), args); err == nil && !strings.Contains(res.Content, "error") {
		t.Fatalf("write into a read root succeeded: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(other, "new.md")); err == nil {
		t.Fatal("a file was created in a read root")
	}
}
