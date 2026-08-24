// Package config holds the settings an operator would otherwise have to
// remember as environment variables.
//
// Five variables with no way to list them is a setup nobody can hold in their
// head, and a missing one surfaces as a refusal in the middle of a session.
// The file makes the whole set visible and editable in one place.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// File is the on-disk settings.
//
// Every field maps to a flag and an environment variable. The order of
// precedence is flag, then environment, then this file: what was typed for
// this run beats what was exported for this shell, which beats what was saved
// once.
type File struct {
	BaseURL       string `yaml:"base_url,omitempty"`
	Model         string `yaml:"model,omitempty"`
	APIKey        string `yaml:"api_key,omitempty"`
	Reasoning     string `yaml:"reasoning,omitempty"`
	VisionModel   string `yaml:"vision_model,omitempty"`
	VisionBaseURL string `yaml:"vision_base_url,omitempty"`
}

// Setting describes one field, so the same list drives display, editing and
// the mapping to environment variables.
type Setting struct {
	Key    string
	Env    string
	Doc    string
	Secret bool
	get    func(*File) string
	set    func(*File, string)
}

// Settings is every setting, in the order they matter when starting out.
var Settings = []Setting{
	{
		Key: "model", Env: "SKODE_MODEL",
		Doc: "model name as the server knows it",
		get: func(f *File) string { return f.Model },
		set: func(f *File, v string) { f.Model = v },
	},
	{
		Key: "base_url", Env: "SKODE_BASE_URL",
		Doc: "OpenAI-compatible endpoint, including the version segment",
		get: func(f *File) string { return f.BaseURL },
		set: func(f *File, v string) { f.BaseURL = v },
	},
	{
		Key: "api_key", Env: "SKODE_API_KEY",
		Doc: "bearer token, when the server requires one", Secret: true,
		get: func(f *File) string { return f.APIKey },
		set: func(f *File, v string) { f.APIKey = v },
	},
	{
		Key: "vision_model", Env: "SKODE_VISION_MODEL",
		Doc: "model that reads images, for /image; the coding model rarely can",
		get: func(f *File) string { return f.VisionModel },
		set: func(f *File, v string) { f.VisionModel = v },
	},
	{
		Key: "vision_base_url", Env: "SKODE_VISION_BASE_URL",
		Doc: "endpoint for the vision model, when it differs from base_url",
		get: func(f *File) string { return f.VisionBaseURL },
		set: func(f *File, v string) { f.VisionBaseURL = v },
	},
	{
		Key: "reasoning", Env: "SKODE_REASONING",
		Doc: "how much a reasoning model thinks: none, low, medium, high",
		get: func(f *File) string { return f.Reasoning },
		set: func(f *File, v string) { f.Reasoning = v },
	},
}

// Path is where the settings live.
func Path() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "skode", "config.yaml"), nil
}

// Load reads the settings. A missing file is the zero value, not an error.
func Load() (File, error) {
	var f File
	path, err := Path()
	if err != nil {
		return f, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Save writes the settings, creating the directory if needed.
func (f File) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	// 0600: the file holds an API key.
	return os.WriteFile(path, b, 0o600)
}

// Get returns a setting's saved value.
func (f File) Get(key string) (string, bool) {
	for _, s := range Settings {
		if s.Key == key {
			return s.get(&f), true
		}
	}
	return "", false
}

// Set changes a setting, reporting false for a name that is not one.
func (f *File) Set(key, value string) bool {
	for _, s := range Settings {
		if s.Key == key {
			s.set(f, strings.TrimSpace(value))
			return true
		}
	}
	return false
}

// Mask hides all but the last four characters of a secret, which is enough to
// tell two keys apart without printing either.
func Mask(v string) string {
	if v == "" {
		return ""
	}
	if len(v) <= 4 {
		return strings.Repeat("•", len(v))
	}
	return strings.Repeat("•", 8) + v[len(v)-4:]
}
