package tools

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// RedactedSecret replaces a secret in anything the model or the person sees.
const RedactedSecret = "[secret redacted]"

// SecretRedactor masks secrets in tool output before the model reads it:
// the values held in the workspace's secret files and in this process's
// credential variables, and anything shaped like a key or a token. A secret
// found by a search the guard could not foresee is still never shown.
type SecretRedactor struct {
	Root string

	mu      sync.Mutex
	values  []string
	refresh time.Time
}

// minSecretLen keeps short values ("true", "8080") from masking ordinary text.
const minSecretLen = 8

var secretShapes = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
}

// credentialInURL keeps the scheme and user of "scheme://user:password@host".
var credentialInURL = regexp.MustCompile(`([a-z][a-z0-9+.-]*://[^\s:/@]+:)[^\s@/]+@`)

var secretNameHints = []string{"PASSWORD", "PASSWD", "SECRET", "TOKEN", "API_KEY", "APIKEY", "PRIVATE_KEY", "CREDENTIAL", "_KEY"}

// Redact returns text with every secret replaced, and whether any was.
func (s *SecretRedactor) Redact(text string) (string, bool) {
	out := text
	for _, v := range s.knownValues() {
		out = strings.ReplaceAll(out, v, RedactedSecret)
	}
	for _, re := range secretShapes {
		out = re.ReplaceAllString(out, RedactedSecret)
	}
	out = credentialInURL.ReplaceAllString(out, "${1}"+RedactedSecret+"@")
	return out, out != text
}

func (s *SecretRedactor) knownValues() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.refresh) < 10*time.Second {
		return s.values
	}
	seen := map[string]bool{}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if looksSecretName(name) {
			addSecretValue(seen, value)
		}
	}
	if s.Root != "" {
		_ = filepath.WalkDir(s.Root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != s.Root && (isSkippedDir(d.Name()) || strings.Count(strings.TrimPrefix(path, s.Root), string(filepath.Separator)) > 4) {
					return filepath.SkipDir
				}
				return nil
			}
			if isSecretPath(path) {
				for _, v := range secretFileValues(path) {
					addSecretValue(seen, v)
				}
			}
			return nil
		})
	}
	values := make([]string, 0, len(seen))
	for v := range seen {
		values = append(values, v)
	}
	// Longest first, so a value containing another is masked whole.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	s.values, s.refresh = values, time.Now()
	return values
}

func looksSecretName(name string) bool {
	upper := strings.ToUpper(name)
	for _, hint := range secretNameHints {
		if strings.Contains(upper, hint) {
			return true
		}
	}
	return false
}

func addSecretValue(seen map[string]bool, value string) {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	if len(value) >= minSecretLen && !strings.HasPrefix(value, "${") {
		seen[value] = true
	}
}

// secretFileValues reads the values a dotenv or "key: value" file gives to
// keys named like secrets; a key file (a PEM) is masked by its shape instead.
func secretFileValues(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size() > 1<<20 {
		return nil
	}
	var values []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			k, v, ok = strings.Cut(line, ": ")
		}
		// A URL or a port in the same file is configuration, and masking it
		// hides what the agent needs to read; a credential in a URL is
		// masked by its shape anyway.
		if ok && looksSecretName(k) {
			values = append(values, v)
		}
	}
	return values
}

func isSkippedDir(name string) bool {
	for _, d := range defaultSkipDirs {
		if d == name {
			return true
		}
	}
	return false
}

// SecretFiles lists the files under root that hold secrets, for a sandbox
// to mount empty.
func SecretFiles(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && (isSkippedDir(d.Name()) || strings.Count(strings.TrimPrefix(path, root), string(filepath.Separator)) > 4) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && isSecretPath(path) {
			files = append(files, path)
		}
		return nil
	})
	return files
}
