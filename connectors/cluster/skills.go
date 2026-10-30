package cluster

import (
	"context"
	"log/slog"

	"github.com/nats-io/nats.go"
)

// SkillsRefresh tells every instance of an agent to reload its skills when
// one of them was asked to.
type SkillsRefresh struct {
	nc      *nats.Conn
	subject string
	self    string
}

// NewSkillsRefresh calls reload whenever another instance announces a
// refresh, until the returned stop is called.
func NewSkillsRefresh(nc *nats.Conn, agentName, instance string, reload func(context.Context)) (*SkillsRefresh, func(), error) {
	r := &SkillsRefresh{nc: nc, subject: "cortex." + token(agentName) + ".skills.refresh", self: instance}
	sub, err := nc.Subscribe(r.subject, func(msg *nats.Msg) {
		if string(msg.Data) == r.self {
			return
		}
		reload(context.Background())
	})
	if err != nil {
		return nil, nil, err
	}
	return r, func() { _ = sub.Unsubscribe() }, nil
}

// Announce tells the other instances to reload.
func (r *SkillsRefresh) Announce(context.Context) {
	if err := r.nc.Publish(r.subject, []byte(r.self)); err != nil {
		slog.Warn("cluster: skills refresh not announced", "error", err)
	}
}
