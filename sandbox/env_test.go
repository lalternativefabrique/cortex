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

func TestInheritedKeepsWindowsEssentials(t *testing.T) {
	got := Inherited([]string{"SystemRoot=C:\\Windows", "APPDATA=x", "API_TOKEN=secret"}, DefaultInherit, nil)
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestInheritedFoldsNamesOnlyWhenTheSystemDoes(t *testing.T) {
	environ := []string{"Path=C:\\bin", "lc_all=C"}

	if got := Inherited(environ, []string{"PATH", "LC_*"}, nil); len(got) != 0 {
		t.Fatalf("case-sensitive match kept %v", got)
	}

	foldEnvNames = true
	t.Cleanup(func() { foldEnvNames = false })
	if got := Inherited(environ, []string{"PATH", "LC_*"}, nil); len(got) != 2 {
		t.Fatalf("case-insensitive match kept %v", got)
	}
}
