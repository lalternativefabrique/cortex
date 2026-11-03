package host

import (
	"context"
	"log/slog"
	"time"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/profile"
)

// learnTimeout bounds the derivation that follows a turn: it is one model
// call, off the turn's path.
const learnTimeout = 45 * time.Second

func (h *Host) personOf(subject string, meta map[string]any) *profile.Scope {
	if h.cfg.Profile == nil || subject == "" || !recallWanted(meta) {
		return nil
	}
	return &profile.Scope{Subject: subject}
}

func (h *Host) withProfile(ctx context.Context, log *slog.Logger, base string, person *profile.Scope) string {
	if person == nil {
		return base
	}
	facts, err := h.cfg.Profile.Facts(ctx, *person)
	if err != nil {
		log.WarnContext(ctx, "cortex: profile not read", "error", err)
		return base
	}
	return instructions(base, profile.Render(facts, profile.MaxRenderBytes))
}

func (h *Host) observe(ctx context.Context, log *slog.Logger, person *profile.Scope, turn Turn, asked string) {
	if person == nil {
		return
	}
	err := h.cfg.Profile.Observe(ctx, profile.Signal{
		Scope: *person, ID: turn.Message.ID, Agent: h.cfg.Agent.Name, Conversation: turn.ContextID,
		Kind: profile.SignalSaid, Content: asked, At: time.Now().UTC(),
	})
	if err != nil {
		log.WarnContext(ctx, "cortex: signal not observed", "error", err)
	}
}

// learn derives the person's profile from the turn just answered, in the
// background; Close waits for it.
func (h *Host) learn(ctx context.Context, log *slog.Logger, client agent.Client, person *profile.Scope) {
	if person == nil {
		return
	}
	h.learning.Add(1)
	go func() {
		defer h.learning.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), learnTimeout)
		defer cancel()
		d := &profile.Deriver{Client: client, Store: h.cfg.Profile}
		changed, err := d.Learn(ctx, *person)
		switch {
		case err != nil:
			log.WarnContext(ctx, "cortex: profile not learned", "error", err)
		case changed > 0:
			log.InfoContext(ctx, "cortex: profile learned", "changes", changed)
		}
	}()
}
