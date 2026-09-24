package tools

import "testing"

func TestInspectionCommandsAreReadOnly(t *testing.T) {
	for _, line := range []string{
		`grep -n "func add" main.go`,
		"cd /tmp && ls -la",
		"git status",
		"go vet ./...",
	} {
		if !isReadOnlyCommand(line) {
			t.Errorf("%q reads only and should qualify", line)
		}
	}
}

func TestWriteFlagsDisqualifyAReadCommand(t *testing.T) {
	for _, line := range []string{
		"find . -name '*.tmp' -delete",
		"sed -i s/a/b/ file.go",
	} {
		if isReadOnlyCommand(line) {
			t.Errorf("%q writes despite a harmless leading word", line)
		}
	}
	if !isReadOnlyCommand("sed -n 1,20p file.go") {
		t.Error("sed -n only prints and should pass")
	}
}

func TestRedirectionDisqualifiesAReadCommand(t *testing.T) {
	if isReadOnlyCommand("cat a.go > b.go") {
		t.Error("a redirection writes whatever produced the bytes")
	}
}

func TestGlobMatchesIntermediateSegments(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"apps/sklp/**/ociproc/**", "apps/sklp/space/ociproc/spec.go", true},
		{"**/ociproc/**", "apps/sklp/space/ociproc/spec.go", true},
		{"apps/**/*.go", "apps/core/code/task.go", true},
		{"apps/sklp/**/ociproc/**", "apps/other/space/ociproc/spec.go", false},
		{"**/*_test.go", "a/b/c/thing_test.go", true},
		{"src/**", "src/a/b.ts", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.path); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}
