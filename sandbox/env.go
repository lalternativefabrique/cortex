package sandbox

import (
	"os"
	"runtime"
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
	"SystemRoot", "SystemDrive", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP", "OS",
	"USERNAME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA",
	"ProgramData", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432",
	"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
}

// Windows treats variable names as case-insensitive and commonly spells
// PATH as "Path".
var foldEnvNames = runtime.GOOS == "windows"

// Inherited filters environ down to the names allow lists, plus extra.
func Inherited(environ, allow, extra []string) []string {
	names := append(append([]string{}, allow...), extra...)
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		for _, n := range names {
			if nameMatches(name, n) {
				out = append(out, kv)
				break
			}
		}
	}
	return out
}

func nameMatches(name, pattern string) bool {
	if foldEnvNames {
		name, pattern = strings.ToUpper(name), strings.ToUpper(pattern)
	}
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(name, prefix)
	}
	return name == pattern
}

func (d *Direct) environ() []string {
	if d.Inherit == nil {
		return os.Environ()
	}
	return Inherited(os.Environ(), d.Inherit, d.Passthrough)
}
