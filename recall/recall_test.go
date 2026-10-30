package recall

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lalternative/packages/go/cortex/agent"
)

type memStore struct {
	mu      sync.Mutex
	entries []Entry
	fail    error
}

func (m *memStore) Remember(_ context.Context, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.entries = append(m.entries, e)
	return nil
}

func (m *memStore) Recall(_ context.Context, s Scope, query string, limit int) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Entry
	for _, e := range m.entries {
		if e.Scope == s && strings.Contains(e.Content, query) && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memStore) Forget(context.Context, Scope, string) error { return nil }

var marie = Scope{Subject: "marie", Agent: "ego"}

func TestTheToolRecallsOnlyWithinItsScope(t *testing.T) {
	at := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	store := &memStore{entries: []Entry{
		{Scope: marie, Kind: Said, Role: "user", Content: "budget salon: 5000 €", At: at},
		{Scope: Scope{Subject: "paul", Agent: "ego"}, Kind: Said, Role: "user", Content: "budget salon: 9000 €", At: at},
		{Scope: Scope{Subject: "marie", Agent: "veille"}, Kind: Said, Role: "user", Content: "budget salon: 1 €", At: at},
	}}
	res, err := NewTool(store, marie).Execute(context.Background(), json.RawMessage(`{"query":"budget salon"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "[2026-09-03] said user: budget salon: 5000 €\n" {
		t.Fatalf("content = %q: only marie's memory with ego may come back", res.Content)
	}
}

func TestTheToolRefusesAnEmptyQuery(t *testing.T) {
	res, _ := NewTool(&memStore{}, marie).Execute(context.Background(), json.RawMessage(`{"query":"  "}`))
	if !strings.HasPrefix(res.Content, "error:") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestTheRecorderRemembersWhatTheAgentDid(t *testing.T) {
	store := &memStore{}
	r := NewRecorder(context.Background(), store, marie, "conv-1")
	var cb agent.Callback = r

	r.Said("m1", "user", "rappelle-moi lundi")
	r.Said("m2", "assistant", "  ")
	cb.OnToolEnd(agent.ToolCallTrace{Name: "remind_me", Arguments: `{"in":"72h"}`, Result: "reminder set"})
	cb.OnToolEnd(agent.ToolCallTrace{Name: "create_note", Arguments: `{}`, Err: "notes unavailable"})
	cb.OnToolEnd(agent.ToolCallTrace{Name: toolName, Arguments: `{"query":"x"}`, Result: "nothing found"})
	r.Produced("m3", "rappel posé pour lundi 9 h")

	if len(store.entries) != 4 {
		t.Fatalf("entries = %+v, want said, two did, produced", store.entries)
	}
	did := store.entries[1]
	if did.Kind != Did || did.Role != "remind_me" || did.Content != `remind_me({"in":"72h"}) -> reminder set` || did.Conversation != "conv-1" || did.Scope != marie {
		t.Fatalf("did = %+v", did)
	}
	if !strings.HasSuffix(store.entries[2].Content, "failed: notes unavailable") {
		t.Fatalf("failed action = %q", store.entries[2].Content)
	}
	if store.entries[3].Kind != Produced {
		t.Fatalf("last = %+v", store.entries[3])
	}
}

func TestAFailingStoreNeverBreaksTheRun(t *testing.T) {
	r := NewRecorder(context.Background(), &memStore{fail: errors.New("down")}, marie, "c")
	r.OnToolEnd(agent.ToolCallTrace{Name: "weather", Result: "sunny"})
	var nilRecorder *Recorder
	nilRecorder.Said("m", "user", "x")
}

func TestALongActionIsCutOnACharacterBoundary(t *testing.T) {
	store := &memStore{}
	r := NewRecorder(context.Background(), store, marie, "c")
	r.OnToolEnd(agent.ToolCallTrace{Name: "fetch_url", Result: strings.Repeat("é", 1000)})
	got := store.entries[0].Content
	if !utf8.ValidString(got) || len(got) > maxDidBytes+len("…") {
		t.Fatalf("truncated to %d bytes, valid utf-8 = %v", len(got), utf8.ValidString(got))
	}
}
