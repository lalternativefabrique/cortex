package shell

import (
	"strings"
	"testing"
)

func TestParseShellReadsWordsRedirectionsAndStages(t *testing.T) {
	stages, err := Parse(`grep -n "func add" main.go 2>/dev/null | sort -o out ; echo 'a b' >> log && cat < in`)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 4 {
		t.Fatalf("got %d stages: %+v", len(stages), stages)
	}
	if got := stages[0].String(); got != "grep -n func add main.go" || stages[0].Writes[0] != "/dev/null" {
		t.Errorf("stage 0 = %q writes %v", got, stages[0].Writes)
	}
	if got := stages[2].String(); got != "echo a b" || stages[2].Writes[0] != "log" {
		t.Errorf("stage 2 = %q writes %v", got, stages[2].Writes)
	}
	if stages[3].Reads[0] != "in" {
		t.Errorf("stage 3 reads %v", stages[3].Reads)
	}
}

func TestParseShellRefusesWhatItCannotSeeThrough(t *testing.T) {
	for _, line := range []string{
		"echo $(id)", "echo `id`", "echo ${X}", "echo $X", `echo "$X"`,
		"cat <<EOF\nx\nEOF", "cat <<< x", "diff <(ls) <(ls)",
		"(cd /; ls)", "{ ls; }", "echo {a,b}",
		"for f in a b; do echo; done", "if true; then ls; fi",
		"ls # && rm -rf /", "ls \\\n rm x", "echo 'open",
		"'rm' -rf x", "r\\m -rf x", "\"git push\" origin",
		"PATH=/tmp/evil ls", "LD_PRELOAD=./x ls", "GIT_SSH_COMMAND=x git fetch", "GOFLAGS=-toolexec=./x go vet",
		"ls ;; rm x", "&& ls", "ls |", "ls > ", "echo \r",
	} {
		if _, err := Parse(line); err == nil {
			t.Errorf("%q parsed, want opaque", line)
		}
	}
}

func TestParseShellKeepsHarmlessAssignmentsAndQuotedArguments(t *testing.T) {
	stages, err := Parse(`GOWORK=off CGO_ENABLED=0 go test -run 'TestX|TestY' ./... 2>&1`)
	if err != nil {
		t.Fatal(err)
	}
	if got := stages[0].Command().String(); got != "go test -run TestX|TestY ./..." {
		t.Errorf("got %q", got)
	}
	if strings.Join(stages[0].Writes, ",") != "" {
		t.Errorf("2>&1 is a duplication, not a write: %v", stages[0].Writes)
	}
}
