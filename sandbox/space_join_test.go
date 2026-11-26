package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeSklp(t *testing.T, script string) (bin, state string) {
	t.Helper()
	state = t.TempDir()
	bin = filepath.Join(t.TempDir(), "sklp")
	must(t, os.WriteFile(bin, []byte("#!/bin/sh\nstate="+state+"\n"+script), 0o755))
	return bin, state
}

func TestAgentSpaceNameMatchesSklp(t *testing.T) {
	if got := AgentSpaceName("/Users/syl/code/messag"); got != "agent-messag-d900d0e7" {
		t.Fatalf("got %s", got)
	}
}

func TestSpaceJoinsASpaceAnotherAgentHolds(t *testing.T) {
	sklp, state := fakeSklp(t, `case "$1 $2" in
"agent up") touch $state/opened; exit 1 ;;
"space exec") [ "$5" = true ] && exit 0; echo joined ;;
esac
`)
	s := &Space{Sklp: sklp, Root: t.TempDir()}
	out, err := s.Run(context.Background(), Command{Line: "echo hi"})
	if err != nil || !strings.Contains(out.Stdout, "joined") {
		t.Fatalf("out %+v err %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(state, "opened")); err == nil {
		t.Fatal("the agent opened the space a second time")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSpaceFreesABusySpaceAndOpensIt(t *testing.T) {
	sklp, state := fakeSklp(t, `case "$1 $2" in
"agent up")
  if [ -e $state/stopped ]; then echo "agent space name=agent-x-1"; touch $state/opened; cat >/dev/null; exit 0; fi
  echo "agent space name=agent-x-1"; echo "mount overlay: device or resource busy" >&2; exit 1 ;;
"space stop") touch $state/stopped ;;
"space exec") [ -e $state/opened ] || exit 1; [ "$5" = true ] && exit 0; echo ran ;;
esac
`)
	s := &Space{Sklp: sklp, Root: t.TempDir(), StartTimeout: 20 * time.Second}
	t.Cleanup(func() { _ = s.Close() })
	out, err := s.Run(context.Background(), Command{Line: "echo hi"})
	if err != nil || !strings.Contains(out.Stdout, "ran") {
		t.Fatalf("out %+v err %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(state, "stopped")); err != nil {
		t.Fatal("the busy space was not stopped first")
	}
}
