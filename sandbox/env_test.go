package sandbox

import (
	"context"
	"strings"
	"testing"
)

func TestDirectKeepsWhatToolsNeedAndDropsCredentials(t *testing.T) {
	t.Setenv("LALTER_TOKEN", "secret-token")
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("CHOSEN_BY_OPERATOR", "kept")
	d := &Direct{Shell: "/bin/sh", Inherit: DefaultInherit, Passthrough: []string{"CHOSEN_BY_OPERATOR"}}
	out, err := d.Run(context.Background(), Command{Line: "env", Env: []string{"SET_BY_TOOL=1"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"secret-token", "sk-secret"} {
		if strings.Contains(out.Stdout, leaked) {
			t.Errorf("a credential reached the command: %s", leaked)
		}
	}
	for _, kept := range []string{"PATH=", "GOFLAGS=-mod=mod", "CHOSEN_BY_OPERATOR=kept", "SET_BY_TOOL=1"} {
		if !strings.Contains(out.Stdout, kept) {
			t.Errorf("%s was dropped", kept)
		}
	}
}
