package host

import (
	"context"
	"sync"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
)

// maxConversations bounds what one container keeps in memory; the least
// recently spoken conversation is dropped first.
const maxConversations = 2000

// conversations is each A2A context's history. It lives with the process: a
// restarted container starts every context afresh.
type conversations struct {
	mu    sync.Mutex
	limit int
	byID  map[string]*conversation
}

type conversation struct {
	messages []agent.Message
	used     time.Time
}

func newConversations(limit int) *conversations {
	return &conversations{limit: limit, byID: map[string]*conversation{}}
}

// History keeps each conversation's messages, for whichever instance runs
// its next turn.
type History interface {
	Load(ctx context.Context, conversation string) ([]agent.Message, error)
	Append(ctx context.Context, conversation string, messages ...agent.Message) error
}

// NewMemoryHistory keeps conversations in this process, the least recently
// spoken dropped first past limit.
func NewMemoryHistory(limit int) History {
	if limit <= 0 {
		limit = maxConversations
	}
	return newConversations(limit)
}

func (c *conversations) Load(_ context.Context, id string) ([]agent.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conv, ok := c.byID[id]; ok {
		return append([]agent.Message(nil), conv.messages...), nil
	}
	return nil, nil
}

func (c *conversations) Append(_ context.Context, id string, messages ...agent.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	conv, ok := c.byID[id]
	if !ok {
		if len(c.byID) >= c.limit {
			c.evictOldest()
		}
		conv = &conversation{}
		c.byID[id] = conv
	}
	conv.messages = append(conv.messages, messages...)
	conv.used = time.Now()
	return nil
}

func (c *conversations) evictOldest() {
	var oldest string
	var at time.Time
	for id, conv := range c.byID {
		if oldest == "" || conv.used.Before(at) {
			oldest, at = id, conv.used
		}
	}
	delete(c.byID, oldest)
}
