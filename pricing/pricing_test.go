package pricing

import (
	"testing"
)

func TestTheRateIsAppliedPerMillionTokens(t *testing.T) {
	table := Table{"m": {Input: 1.00, Output: 2.00}}
	cost, known := table.Cost("m", 1_000_000, 0, 500_000)
	if !known {
		t.Fatal("a known model reported no price")
	}
	if want := 1.00 + 1.00; cost != want {
		t.Fatalf("cost = %v, want %v", cost, want)
	}
}
func TestAnUnknownModelCostsNothingAndSaysSo(t *testing.T) {
	// Inventing a figure is worse than admitting ignorance: the caller would
	// act on it.
	cost, known := Table{}.Cost("no-such-model", 1_000_000, 0, 1_000_000)
	if known {
		t.Fatal("an unknown model reported a price")
	}
	if cost != 0 {
		t.Fatalf("cost = %v, want zero for an unknown model", cost)
	}
}
func TestALookupIgnoresTheTagAServerAppends(t *testing.T) {
	table := Table{}
	table.Set("Devstral", Rate{Input: 1, Output: 2})
	if _, known := table.Cost("devstral:latest", 0, 0, 0); !known {
		t.Fatal("a tagged model name did not find its rate")
	}
}
