package serve

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/profile"
)

// client is the model with its fallbacks behind it.
func (s *Server) client() (agent.Client, error) {
	c, err := agent.NewClient(s.cfg.Provider)
	if err != nil {
		return nil, err
	}
	return agent.WithFallbacks(c, s.cfg.Fallbacks...)
}

// learnTimeout bounds the derivation that follows a turn: one model call,
// off the turn's path.
const learnTimeout = 45 * time.Second

// person is whose profile this machine keeps: its one person, as Recall
// scopes their memory with an empty scope.
var person = profile.Scope{}

func (s *Server) withProfile(ctx context.Context, system string, incognito bool) string {
	if s.cfg.Profile == nil || incognito {
		return system
	}
	facts, err := s.cfg.Profile.Facts(ctx, person)
	if err != nil {
		slog.Warn("serve: profile not read", "error", err)
		return system
	}
	block := profile.Render(facts, profile.MaxRenderBytes)
	if block == "" {
		return system
	}
	return system + "\n\n" + block
}

func (s *Server) observe(ctx context.Context, conversation, question string, incognito bool) {
	if s.cfg.Profile == nil || incognito {
		return
	}
	err := s.cfg.Profile.Observe(ctx, profile.Signal{
		Scope: person, ID: uuid.NewString(), Conversation: conversation,
		Kind: profile.SignalSaid, Content: profile.Clip(question, profile.MaxSignalBytes), At: time.Now().UTC(),
	})
	if err != nil {
		slog.Warn("serve: signal not observed", "error", err)
	}
}

func (s *Server) learn(ctx context.Context, client agent.Client, incognito bool) {
	if s.cfg.Profile == nil || incognito {
		return
	}
	s.learning.Add(1)
	go func() {
		defer s.learning.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), learnTimeout)
		defer cancel()
		d := &profile.Deriver{Client: client, Store: s.cfg.Profile}
		changed, err := d.Learn(ctx, person)
		switch {
		case err != nil:
			slog.Warn("serve: profile not learned", "error", err)
		case changed > 0:
			slog.Info("serve: profile learned", "changes", changed)
		}
	}()
}
