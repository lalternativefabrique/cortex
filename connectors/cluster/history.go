package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/host"
)

// maxHistoryBytes keeps one conversation under JetStream's default message
// size, the oldest exchanges dropped first.
const maxHistoryBytes = 768 << 10

// History is host.History in a JetStream key-value bucket.
type History struct {
	kv jetstream.KeyValue
}

var _ host.History = (*History)(nil)

// NewHistory keeps an agent's conversations for ttl after their last turn.
func NewHistory(ctx context.Context, js jetstream.JetStream, agentName string, ttl time.Duration) (*History, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  "cortex_" + token(agentName) + "_history",
		TTL:     ttl,
		Storage: jetstream.FileStorage,
	})
	if err != nil {
		return nil, fmt.Errorf("cluster: history bucket: %w", err)
	}
	return &History{kv: kv}, nil
}

func (h *History) Load(ctx context.Context, conversation string) ([]agent.Message, error) {
	entry, err := h.kv.Get(ctx, hashed(conversation))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var messages []agent.Message
	if err := json.Unmarshal(entry.Value(), &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// Append adds to a conversation. Its turns run one at a time, so the read
// and the write below never race another turn of it.
func (h *History) Append(ctx context.Context, conversation string, messages ...agent.Message) error {
	past, err := h.Load(ctx, conversation)
	if err != nil {
		return err
	}
	all := append(past, messages...)
	for {
		data, err := json.Marshal(all)
		if err != nil {
			return err
		}
		if len(data) <= maxHistoryBytes || len(all) <= len(messages) {
			_, err = h.kv.Put(ctx, hashed(conversation), data)
			return err
		}
		all = all[min(2, len(all)-len(messages)):]
	}
}
