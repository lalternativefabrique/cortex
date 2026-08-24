package tools

import "testing"

func TestPlanModeAllowsInspection(t *testing.T) {
	// Plan means look but do not touch, not do nothing: refusing a read
	// costs a step and teaches the model nothing.
	g := &GatedApprover{Mode: ModePlan, Ask: &recordingApprover{decision: Deny}}
	for _, line := range []string{
		`grep -n "func add" main.go`,
		"cd /tmp && ls -la",
		"git status",
		"go vet ./...",
	} {
		if decide(t, g, bashReq(line)) != Allow {
			t.Errorf("%q reads only and should run in plan mode", line)
		}
	}
}

func TestPlanModeStillRefusesWrites(t *testing.T) {
	g := &GatedApprover{
		Mode:  ModePlan,
		Root:  "/w",
		Rules: Rules{AllowWrite: []string{"**/*.go"}},
		Ask:   &recordingApprover{decision: Allow},
	}
	// An allow_write glob must not open a hole in a read-only run.
	if decide(t, g, Request{Tool: "edit", Scope: "edit:/w/a.go"}) != Deny {
		t.Fatal("plan mode allowed an edit through allow_write")
	}
	if decide(t, g, bashReq("rm -f x")) != Deny {
		t.Fatal("plan mode allowed a mutating command")
	}
}

func TestWriteFlagsDisqualifyAReadCommand(t *testing.T) {
	g := &GatedApprover{Mode: ModeAuto, Ask: &recordingApprover{decision: Deny}}
	for _, line := range []string{
		"find . -name '*.tmp' -delete",
		"sed -i s/a/b/ file.go",
	} {
		if decide(t, g, bashReq(line)) == Allow {
			t.Errorf("%q writes despite a harmless leading word", line)
		}
	}
	if decide(t, g, bashReq("sed -n 1,20p file.go")) != Allow {
		t.Error("sed -n only prints and should pass")
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
