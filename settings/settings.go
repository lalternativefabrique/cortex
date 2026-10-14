// Package settings reads what a person and a project decided about an agent
// on a workstation: its mode, its permission rules and its hooks, in the
// shape Claude Code settings have.
//
// Three files are read in order, each adding to the last: the person's
// (~/.config/<app>/settings.yaml), the project's (<root>/.ai/settings.yaml,
// committed) and the machine's (<root>/.ai/settings.local.yaml, not
// committed). Rules accumulate and deny wins whatever file it came from; the
// mode is the last one set.
package settings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/lalternative/packages/go/cortex/tools"
)

// File is one settings file as written.
type File struct {
	Mode        string `yaml:"mode,omitempty"`
	Permissions struct {
		Allow []string `yaml:"allow,omitempty"`
		Ask   []string `yaml:"ask,omitempty"`
		Deny  []string `yaml:"deny,omitempty"`
	} `yaml:"permissions,omitempty"`
	Hooks struct {
		PreToolUse []tools.Hook `yaml:"pre_tool_use,omitempty"`
	} `yaml:"hooks,omitempty"`
}

// Rule is a parsed rule and the file it came from, so a surprising decision
// can be explained.
type Rule struct {
	tools.Rule
	Origin string
}

type Settings struct {
	Mode       tools.Mode
	ModeOrigin string
	Allow      []Rule
	Ask        []Rule
	Deny       []Rule
	PreToolUse []tools.Hook
	// Files lists the files that were found, in the order they were read.
	Files []string
}

// Paths are the files Load reads for app on root, nearest last.
func Paths(app, root string) []string {
	var out []string
	if user, err := userFile(app); err == nil {
		out = append(out, user)
	}
	if root != "" {
		out = append(out,
			filepath.Join(root, ".ai", "settings.yaml"),
			filepath.Join(root, ".ai", "settings.local.yaml"))
	}
	return out
}

func userFile(app string) (string, error) {
	if app == "" {
		return "", errors.New("no app name")
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, app, "settings.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", app, "settings.yaml"), nil
}

// Load reads every file of Paths that exists. A missing file is nothing; a
// malformed one is an error naming it, since a rule silently dropped is a
// permission silently granted.
func Load(app, root string) (Settings, error) {
	return LoadFiles(Paths(app, root))
}

func LoadFiles(paths []string) (Settings, error) {
	s := Settings{Mode: tools.ModeAsk}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return s, fmt.Errorf("read %s: %w", path, err)
		}
		var f File
		if err := yaml.Unmarshal(b, &f); err != nil {
			return s, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := s.add(f, path); err != nil {
			return s, fmt.Errorf("%s: %w", path, err)
		}
		s.Files = append(s.Files, path)
	}
	return s, nil
}

func (s *Settings) add(f File, origin string) error {
	if f.Mode != "" {
		mode, err := tools.ParseMode(f.Mode)
		if err != nil {
			return err
		}
		s.Mode, s.ModeOrigin = mode, origin
	}
	var err error
	if s.Allow, err = appendRules(s.Allow, f.Permissions.Allow, origin); err != nil {
		return err
	}
	if s.Ask, err = appendRules(s.Ask, f.Permissions.Ask, origin); err != nil {
		return err
	}
	if s.Deny, err = appendRules(s.Deny, f.Permissions.Deny, origin); err != nil {
		return err
	}
	s.PreToolUse = append(s.PreToolUse, f.Hooks.PreToolUse...)
	return nil
}

func appendRules(to []Rule, lines []string, origin string) ([]Rule, error) {
	for _, l := range lines {
		r, err := tools.ParseRule(l)
		if err != nil {
			return to, err
		}
		to = append(to, Rule{Rule: r, Origin: origin})
	}
	return to, nil
}

// Rules is what the approver takes.
func (s Settings) Rules() tools.Rules {
	return tools.Rules{Allow: bare(s.Allow), Ask: bare(s.Ask), Deny: bare(s.Deny)}
}

func bare(rules []Rule) []tools.Rule {
	out := make([]tools.Rule, len(rules))
	for i, r := range rules {
		out[i] = r.Rule
	}
	return out
}

// Approver builds the workstation gate these settings describe: hooks first,
// then the mode and the rules, then ask.
func (s Settings) Approver(root string, ask tools.Approver) tools.Approver {
	return tools.WithHooks(&tools.GatedApprover{Mode: s.Mode, Rules: s.Rules(), Root: root, Ask: ask}, s.PreToolUse, root)
}
