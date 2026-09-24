package host

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/mcp"
)

const retryServersEvery = 5 * time.Second

// servers holds the MCP servers a host was configured with. One unreachable
// at start, typically the app a sidecar runs beside still booting, is tried
// again before a later turn instead of being lost until a restart.
type servers struct {
	base  context.Context
	retry time.Duration

	mu       sync.Mutex
	sessions []*mcp.Session
	tools    []agent.Tool
	missing  []mcp.ServerConfig
	tried    time.Time
}

// startServers connects under base, which outlives every turn: a stdio
// server started for one turn must not die with it.
func startServers(base context.Context, cfg mcp.Config) *servers {
	s := &servers{base: base, retry: retryServersEvery}
	seen := map[string]bool{}
	for _, sc := range cfg.Servers {
		if !seen[sc.Name] {
			seen[sc.Name] = true
			s.missing = append(s.missing, sc)
		}
	}
	s.connect()
	return s
}

func (s *servers) connect() {
	var still []mcp.ServerConfig
	for _, sc := range s.missing {
		session, tools, _ := mcp.Start(s.base, mcp.Config{Servers: []mcp.ServerConfig{sc}}, func(err error) {
			slog.Warn("host: mcp server unavailable", "server", sc.Name, "error", err)
		})
		if len(session.Servers()) == 0 {
			still = append(still, sc)
			continue
		}
		s.sessions = append(s.sessions, session)
		s.tools = append(s.tools, tools...)
	}
	s.missing, s.tried = still, time.Now()
}

// forTurn returns the tools a turn may call, first retrying the servers
// still missing.
func (s *servers) forTurn() []agent.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.missing) > 0 && time.Since(s.tried) >= s.retry {
		s.connect()
	}
	return append([]agent.Tool(nil), s.tools...)
}

func (s *servers) offered() []agent.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agent.Tool(nil), s.tools...)
}

func (s *servers) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, session := range s.sessions {
		session.Close()
	}
}
