package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lalternative/packages/go/cortex/agent"
	"gopkg.in/yaml.v3"
)

// Config is the on-disk list of servers to launch.
type Config struct {
	Servers []ServerConfig `yaml:"servers"`
}

// LoadConfig reads the user's server list, then the workspace's, and merges
// them. A repository may add servers its own work needs without the operator
// having to declare them globally.
//
// Both files are optional.
func LoadConfig(root string) (Config, error) {
	var merged Config

	if path, err := userConfigPath(); err == nil {
		c, err := readConfig(path)
		if err != nil {
			return merged, err
		}
		merged.Servers = append(merged.Servers, c.Servers...)
	}

	repo, err := readConfig(filepath.Join(root, ".ai", "mcp.yaml"))
	if err != nil {
		return merged, err
	}
	merged.Servers = append(merged.Servers, repo.Servers...)
	return merged, nil
}

func userConfigPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "skode", "mcp.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "skode", "mcp.yaml"), nil
}

func readConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse %s: %w", path, err)
	}
	for i, s := range c.Servers {
		if s.Name == "" {
			return c, fmt.Errorf("%s: server %d has no name", path, i+1)
		}
		if s.Command == "" {
			return c, fmt.Errorf("%s: server %q has no command", path, s.Name)
		}
	}
	return c, nil
}

// Session holds every connected server for the life of a run.
type Session struct {
	clients []*Client
}

// Start connects to each configured server and collects their tools.
//
// A server that fails to start is reported and skipped rather than aborting
// the run: losing one optional capability is a smaller loss than refusing to
// work at all, and the operator can see what happened.
func Start(ctx context.Context, cfg Config, onError func(error)) (*Session, []agent.Tool, error) {
	s := &Session{}
	var tools []agent.Tool

	for _, sc := range cfg.Servers {
		client, err := Connect(ctx, sc)
		if err != nil {
			if onError != nil {
				onError(err)
			}
			continue
		}
		found, err := client.ListTools(ctx)
		if err != nil {
			if onError != nil {
				onError(err)
			}
			client.Close()
			continue
		}
		s.clients = append(s.clients, client)
		tools = append(tools, found...)
	}
	return s, tools, nil
}

// Close shuts down every server.
func (s *Session) Close() {
	for _, c := range s.clients {
		c.Close()
	}
}

// Servers lists the names of the connected servers.
func (s *Session) Servers() []string {
	names := make([]string, 0, len(s.clients))
	for _, c := range s.clients {
		names = append(names, c.Name())
	}
	return names
}
