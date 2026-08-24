package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAMissingFileIsNotAnError(t *testing.T) {
	// Nobody has configured anything on a first run, and refusing to start
	// over an absent file would be the worst possible welcome.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if f != (File{}) {
		t.Fatalf("f = %+v, want the zero value", f)
	}
}

func TestSavedSettingsComeBack(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var f File
	if !f.Set("model", "qwen3-235b") || !f.Set("api_key", "secret") {
		t.Fatal("a known setting was refused")
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := back.Get("model"); v != "qwen3-235b" {
		t.Fatalf("model = %q", v)
	}
}

func TestTheFileIsNotWorldReadable(t *testing.T) {
	// It holds an API key.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := (File{APIKey: "secret"}).Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "skode", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
}

func TestAnUnknownSettingIsRefused(t *testing.T) {
	var f File
	if f.Set("colour", "blue") {
		t.Fatal("a name that is not a setting was accepted")
	}
}

func TestASecretIsNeverPrintedWhole(t *testing.T) {
	const key = "ad8cfd59-1b28-4df8-a170-9fe6a8a4b1b1"
	masked := Mask(key)
	if strings.Contains(masked, "ad8cfd59") {
		t.Fatalf("Mask leaked the key: %q", masked)
	}
	// The last four stay, which is enough to tell two keys apart.
	if !strings.HasSuffix(masked, "b1b1") {
		t.Fatalf("Mask = %q, want the tail kept", masked)
	}
}

func TestEverySettingHasAnEnvironmentVariable(t *testing.T) {
	// The file sits under the environment, so a setting with no variable
	// could not be overridden for one run.
	for _, s := range Settings {
		if !strings.HasPrefix(s.Env, "SKODE_") {
			t.Errorf("%s: env = %q, want a SKODE_ variable", s.Key, s.Env)
		}
		if s.Doc == "" {
			t.Errorf("%s has no description; /config would show a bare name", s.Key)
		}
	}
}
