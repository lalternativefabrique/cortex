// Package skills loads reusable instructions the operator invokes by name.
//
// A skill is a markdown file with a little frontmatter, the same shape Claude
// Code uses, so the ones an operator already wrote work here unchanged. It
// answers a plain need: a review, a deploy check or an architecture question
// carries the same preamble every time, and retyping it is both tedious and
// unreliable.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is a named set of instructions.
type Skill struct {
	Name        string
	Description string
	// AllowedTools names the tools the skill needs. Empty means all of them.
	// A review skill that cannot write cannot accidentally rewrite the code
	// it was asked to judge.
	AllowedTools []string
	// Model overrides the run's model. Empty leaves the operator's choice
	// alone, which is the safer default: a skill silently switching models
	// changes what a run costs.
	Model string
	// Mode overrides the permission mode, so a read-only skill declares
	// itself rather than relying on the operator remembering -mode plan.
	Mode string
	// Body is the instruction text, frontmatter removed.
	Body string
	// Path is where it was loaded from, for diagnostics.
	Path string
}

type frontmatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	AllowedTools []string `yaml:"allowed-tools"`
	Model        string   `yaml:"model"`
	Mode         string   `yaml:"mode"`
}

// Set is every skill available to a run.
type Set map[string]Skill

// Load reads skills from the user's config and from the repository, with the
// repository winning on a name collision — a project's own version of a
// review is more specific than a general one.
func Load(root string) (Set, error) {
	set := Set{}
	for _, dir := range searchPaths(root) {
		found, err := loadDir(dir)
		if err != nil {
			return set, err
		}
		for name, s := range found {
			set[name] = s
		}
	}
	return set, nil
}

// searchPaths lists skill directories from least to most specific.
func searchPaths(root string) []string {
	var dirs []string
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		dirs = append(dirs, filepath.Join(base, "skode", "skills"))
	} else if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".config", "skode", "skills"))
		// Skills already written for Claude Code work here as-is, so an
		// operator does not maintain two copies.
		dirs = append(dirs, filepath.Join(home, ".claude", "skills"))
	}
	if root != "" {
		dirs = append(dirs, filepath.Join(root, ".ai", "skills"))
	}
	return dirs
}

// loadDir reads both layouts: a flat name.md, and a name/SKILL.md directory.
func loadDir(dir string) (Set, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, nil
	}

	set := Set{}
	for _, e := range entries {
		var path, name string
		switch {
		case e.IsDir():
			path = filepath.Join(dir, e.Name(), "SKILL.md")
			name = e.Name()
		case strings.HasSuffix(e.Name(), ".md"):
			path = filepath.Join(dir, e.Name())
			name = strings.TrimSuffix(e.Name(), ".md")
		default:
			continue
		}
		s, err := parseFile(path)
		if err != nil {
			// One malformed skill must not hide the rest: the operator sees
			// the complaint and keeps working.
			fmt.Fprintf(os.Stderr, "skode: skipping skill %s: %v\n", path, err)
			continue
		}
		if s.Name == "" {
			s.Name = name
		}
		set[strings.ToLower(s.Name)] = s
	}
	return set, nil
}

func parseFile(path string) (Skill, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	meta, body := splitFrontmatter(string(b))

	var fm frontmatter
	if meta != "" {
		if err := yaml.Unmarshal([]byte(meta), &fm); err != nil {
			// Descriptions routinely carry an unquoted colon, which strict
			// YAML rejects and the tools that read these files accept. A
			// skill is worth loading for its body even when its metadata
			// does not parse.
			fm = parseLoosely(meta)
		}
	}
	if strings.TrimSpace(body) == "" {
		return Skill{}, fmt.Errorf("no instructions")
	}
	return Skill{
		Name:         fm.Name,
		Description:  fm.Description,
		AllowedTools: fm.AllowedTools,
		Model:        fm.Model,
		Mode:         fm.Mode,
		Body:         strings.TrimSpace(body),
		Path:         path,
	}, nil
}

// splitFrontmatter separates a leading --- block from the instructions.
func splitFrontmatter(s string) (meta, body string) {
	// A byte-order mark ahead of the frontmatter would hide it.
	s = strings.TrimPrefix(s, "\ufeff")
	if !strings.HasPrefix(s, "---") {
		return "", s
	}
	rest := strings.TrimPrefix(s, "---")
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", s
	}
	meta = rest[:end]
	body = strings.TrimPrefix(rest[end+4:], "\n")
	return meta, body
}

// Lookup finds a skill by name, case-insensitively.
func (s Set) Lookup(name string) (Skill, bool) {
	skill, ok := s[strings.ToLower(strings.TrimPrefix(name, "/"))]
	return skill, ok
}

// Names lists what is available, sorted.
func (s Set) Names() []string {
	out := make([]string, 0, len(s))
	for name := range s {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Prompt combines the skill's instructions with what the operator typed.
//
// The skill goes first and is fenced: it is instruction, and the operator's
// words are the specific request within it. Naming the source lets the model
// say which skill it is following when the two pull apart.
func (k Skill) Prompt(request string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are working under the %q skill, whose instructions follow.\n\n", k.Name)
	b.WriteString("<skill>\n")
	b.WriteString(k.Body)
	b.WriteString("\n</skill>\n")
	if strings.TrimSpace(request) != "" {
		fmt.Fprintf(&b, "\nThe request: %s", strings.TrimSpace(request))
	}
	return b.String()
}

// parseLoosely recovers the scalar fields from frontmatter YAML will not
// accept, taking everything after the first colon as the value.
func parseLoosely(meta string) frontmatter {
	var fm frontmatter
	for _, line := range strings.Split(meta, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(strings.ToLower(key)) {
		case "name":
			fm.Name = value
		case "description":
			fm.Description = value
		case "model":
			fm.Model = value
		case "mode":
			fm.Mode = value
		case "allowed-tools":
			for _, t := range strings.Split(strings.Trim(value, "[]"), ",") {
				if t = strings.TrimSpace(t); t != "" {
					fm.AllowedTools = append(fm.AllowedTools, t)
				}
			}
		}
	}
	return fm
}
