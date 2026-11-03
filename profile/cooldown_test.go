package profile

import (
	"context"
	"testing"
	"time"
)

func TestADerivationWaitsForTheCooldownOfThePerson(t *testing.T) {
	store := &memStore{}
	_ = store.Observe(context.Background(), Signal{Scope: marie, ID: "s1", Kind: SignalSaid, Content: "tutoie-moi", At: start})
	client := &scriptedClient{reply: updateReply(`{"assert":[]}`)}
	now := start
	d := &Deriver{Client: client, Store: store, Cooldown: 10 * time.Minute, Now: func() time.Time { return now }}

	for i := 0; i < 3; i++ {
		if _, err := d.Learn(context.Background(), marie); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if len(client.calls) != 1 {
		t.Fatalf("model calls = %d within the cooldown", len(client.calls))
	}
	now = start.Add(11 * time.Minute)
	if _, err := d.Learn(context.Background(), marie); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("model calls = %d once the cooldown passed", len(client.calls))
	}
	if _, err := d.Learn(context.Background(), Scope{Subject: "paul"}); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 2 {
		t.Fatal("paul has no signals, so no call; and marie's cooldown is not his")
	}

	eager := &Deriver{Client: client, Store: store, Cooldown: -1, Now: func() time.Time { return now }}
	_, _ = eager.Learn(context.Background(), marie)
	_, _ = eager.Learn(context.Background(), marie)
	if len(client.calls) != 4 {
		t.Fatalf("model calls = %d with the cooldown off", len(client.calls))
	}
}
