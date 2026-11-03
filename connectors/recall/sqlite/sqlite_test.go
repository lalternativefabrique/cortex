package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalternativefabrique/cortex/recall"
)

var (
	marie = recall.Scope{Subject: "marie", Agent: "ego"}
	at    = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state", "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func remember(t *testing.T, s *Store, scope recall.Scope, conversation, message, content string, when time.Time) {
	t.Helper()
	if err := s.Remember(context.Background(), recall.Entry{Scope: scope, Conversation: conversation, Message: message, Kind: recall.Said, Role: "user", Content: content, At: when}); err != nil {
		t.Fatal(err)
	}
}

func TestRecallFindsByWordsWithinTheScopeOnly(t *testing.T) {
	s := open(t)
	remember(t, s, marie, "c1", "m1", "budget salon: 5000 €", at)
	remember(t, s, marie, "c1", "m2", "le banquier s'appelle Paul", at.Add(time.Minute))
	remember(t, s, recall.Scope{Subject: "paul", Agent: "ego"}, "c9", "m1", "budget salon: 9000 €", at)
	remember(t, s, recall.Scope{Subject: "marie", Agent: "veille"}, "c9", "m1", "budget salon: 1 €", at)

	found, err := s.Recall(context.Background(), marie, "quel budget pour le salon ?", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Content != "budget salon: 5000 €" || !found[0].At.Equal(at) || found[0].Kind != recall.Said {
		t.Fatalf("found = %+v", found)
	}
}

func TestRecallIgnoresAccentsAndOperators(t *testing.T) {
	s := open(t)
	remember(t, s, marie, "c1", "m1", "la mémoire du projet est dans Notion", at)
	for _, q := range []string{"memoire", `"memoire" AND OR NOT (`, "MÉMOIRE*"} {
		found, err := s.Recall(context.Background(), marie, q, 5)
		if err != nil || len(found) != 1 {
			t.Fatalf("%q: found = %+v, err = %v", q, found, err)
		}
	}
	if found, _ := s.Recall(context.Background(), marie, "?? !", 5); len(found) != 0 {
		t.Fatalf("a query with no word found %+v", found)
	}
}

func TestTheSameMessageIsRememberedOnceAndAConversationCanBeForgotten(t *testing.T) {
	s := open(t)
	remember(t, s, marie, "c1", "m1", "premier essai", at)
	remember(t, s, marie, "c1", "m1", "premier essai, renvoyé", at)
	remember(t, s, marie, "c2", "m1", "autre conversation, même id de message", at)

	found, _ := s.Recall(context.Background(), marie, "essai", 5)
	if len(found) != 1 || found[0].Content != "premier essai" {
		t.Fatalf("found = %+v", found)
	}
	if err := s.Forget(context.Background(), marie, "c1"); err != nil {
		t.Fatal(err)
	}
	if found, _ := s.Recall(context.Background(), marie, "essai", 5); len(found) != 0 {
		t.Fatalf("forgotten conversation still found: %+v", found)
	}
	if found, _ := s.Recall(context.Background(), marie, "conversation", 5); len(found) != 1 {
		t.Fatalf("the other conversation went too: %+v", found)
	}
}

func TestRecallRanksTheClosestFirstAndHonoursTheLimit(t *testing.T) {
	s := open(t)
	remember(t, s, marie, "c1", "m1", "réunion lundi avec le banquier", at)
	remember(t, s, marie, "c1", "m2", "banquier banquier banquier: relancer le banquier", at.Add(time.Minute))
	remember(t, s, marie, "c1", "m3", "courses: pain, lait", at.Add(2*time.Minute))

	found, _ := s.Recall(context.Background(), marie, "banquier", 1)
	if len(found) != 1 || found[0].Message != "m2" {
		t.Fatalf("found = %+v", found)
	}
}

func TestOpenCreatesTheStateDirectoryAndReopensTheSameMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "er", "recall.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	remember(t, first, marie, "c1", "m1", "retenu avant fermeture", at)
	_ = first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if found, _ := second.Recall(context.Background(), marie, "fermeture", 5); len(found) != 1 {
		t.Fatalf("found = %+v", found)
	}
}
