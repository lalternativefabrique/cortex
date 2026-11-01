package sandbox

import (
	"strings"
	"testing"
)

func TestInheritedKeepsWhatToolsNeedAndDropsCredentials(t *testing.T) {
	environ := []string{"LALTER_TOKEN=secret-token", "OPENAI_API_KEY=sk-secret", "GOFLAGS=-mod=mod", "PATH=/bin", "CHOSEN_BY_OPERATOR=kept"}
	got := strings.Join(Inherited(environ, DefaultInherit, []string{"CHOSEN_BY_OPERATOR"}), "\n")
	for _, leaked := range []string{"secret-token", "sk-secret"} {
		if strings.Contains(got, leaked) {
			t.Errorf("a credential reached the command: %s", leaked)
		}
	}
	for _, kept := range []string{"PATH=", "GOFLAGS=-mod=mod", "CHOSEN_BY_OPERATOR=kept"} {
		if !strings.Contains(got, kept) {
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
