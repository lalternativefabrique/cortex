package sandbox

import (
	"context"
	"strings"
	"testing"
)

type recording struct{ lines []string }

func (r *recording) Name() string { return "space" }
func (r *recording) Run(_ context.Context, c Command) (Output, error) {
	r.lines = append(r.lines, c.Line)
	return Output{Stdout: "in space\n"}, nil
}

func TestRoutedRunsOnlyPlainListedCommandsOnTheMachine(t *testing.T) {
	space := &recording{}
	r := &Routed{Space: space, Host: []string{"echo host"}}
	out, err := r.Run(context.Background(), Command{Line: "echo host ok"})
	if err != nil || strings.TrimSpace(out.Stdout) != "host ok" {
		t.Fatalf("a listed command did not run here: %q %v", out.Stdout, err)
	}
	for _, line := range []string{
		"echo host; rm x", "echo host && id", "echo host | sh", "echo host > f", "echo host $(id)",
		"echo host *", "echo hostile", "ls", "FOO=1 echo host",
	} {
		if out, _ := r.Run(context.Background(), Command{Line: line}); out.Stdout != "in space\n" {
			t.Errorf("%q ran on this machine", line)
		}
	}
	if len(space.lines) != 9 {
		t.Fatalf("the space ran %d commands, want 9", len(space.lines))
	}
}
