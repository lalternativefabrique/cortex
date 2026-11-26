package agent

import (
	"encoding/json"
	"testing"
)

func wire(t *testing.T, raw string) Usage {
	t.Helper()
	var w wireUsage
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	return w.usage()
}

func TestUsageSumsTheCostEachProviderReported(t *testing.T) {
	var run Usage
	run.Add(wire(t, `{"prompt_tokens":100,"completion_tokens":10,"cost_eur_micros":1200}`))
	run.Add(wire(t, `{"prompt_tokens":50,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":40},"cost_eur_micros":300}`))
	cost, ok := run.ReportedCost()
	if !ok || cost != 1500 || run.CachedInput != 40 {
		t.Fatalf("cost=%d ok=%v usage=%+v", cost, ok, run)
	}
}

func TestUsageHasNoReportedCostWhenOneCallSaidNothing(t *testing.T) {
	var run Usage
	run.Add(wire(t, `{"prompt_tokens":100,"completion_tokens":10,"cost_eur_micros":1200}`))
	run.Add(wire(t, `{"prompt_tokens":50,"completion_tokens":5}`))
	if _, ok := run.ReportedCost(); ok {
		t.Fatalf("a partial cost was reported as the run's: %+v", run)
	}
}

func TestUsageReportsAFreeCallAsFree(t *testing.T) {
	cost, ok := wire(t, `{"prompt_tokens":100,"completion_tokens":10,"cost_eur_micros":0}`).ReportedCost()
	if !ok || cost != 0 {
		t.Fatalf("cost=%d ok=%v", cost, ok)
	}
}
