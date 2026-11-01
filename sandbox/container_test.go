package sandbox

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestContainerIsRefusedOutsideOneUnlessTheOperatorSaysSo(t *testing.T) {
	t.Setenv("CORTEX_CONFINED", "")
	_, err := NewContainer()
	inOne := err == nil
	t.Setenv("CORTEX_CONFINED", "1")
	c, err := NewContainer()
	if err != nil {
		t.Fatal(err)
	}
	if !inOne && !errors.Is(firstErr(), ErrNotConfined) {
		t.Fatal("outside a container, NewContainer must refuse")
	}
	if c.Name() != "container" {
		t.Fatalf("Name = %q", c.Name())
	}
}

func firstErr() error {
	os.Unsetenv("CORTEX_CONFINED")
	defer os.Setenv("CORTEX_CONFINED", "1")
	_, err := NewContainer()
	return err
}

func TestContainerKeepsWhatToolsNeedAndDropsCredentials(t *testing.T) {
	t.Setenv("LALTER_TOKEN", "secret-token")
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("CHOSEN_BY_OPERATOR", "kept")
	c := &Container{Passthrough: []string{"CHOSEN_BY_OPERATOR"}}
	out, err := c.Run(context.Background(), Command{Line: "env", Env: []string{"SET_BY_TOOL=1"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"secret-token", "sk-secret"} {
		if strings.Contains(out.Stdout, leaked) {
			t.Errorf("a credential reached the command: %s", leaked)
		}
	}
	for _, kept := range []string{"PATH=", "CHOSEN_BY_OPERATOR=kept", "SET_BY_TOOL=1"} {
		if !strings.Contains(out.Stdout, kept) {
			t.Errorf("%s was dropped", kept)
		}
	}
}
