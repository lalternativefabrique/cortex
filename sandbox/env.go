package sandbox

import (
	"os"
	"strings"
)

// DefaultInherit is what a command started on the person's machine keeps
// of their environment: what tools need to run, never what authenticates
// them. A name ending in "*" matches a prefix.
var DefaultInherit = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "TMPDIR", "TZ",
	"LANG", "LANGUAGE", "LC_*",
	"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR",
	"GOPATH", "GOROOT", "GOCACHE", "GOMODCACHE", "GOPRIVATE", "GOPROXY", "GOFLAGS", "GOTOOLCHAIN", "GOWORK", "CGO_ENABLED",
	"CARGO_HOME", "RUSTUP_HOME", "RUSTUP_TOOLCHAIN",
	"PNPM_HOME", "NVM_DIR", "VOLTA_HOME", "NODE_PATH",
	"JAVA_HOME", "PYTHONPATH", "VIRTUAL_ENV",
}

// Inherited filters environ down to the names allow lists, plus extra.
func Inherited(environ, allow, extra []string) []string {
	names := append(append([]string{}, allow...), extra...)
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		for _, n := range names {
			if prefix, ok := strings.CutSuffix(n, "*"); ok && strings.HasPrefix(name, prefix) || name == n {
				out = append(out, kv)
				break
			}
		}
	}
	return out
}

func (d *Direct) environ() []string {
	if d.Inherit == nil {
		return os.Environ()
	}
	return Inherited(os.Environ(), d.Inherit, d.Passthrough)
}
