// Package pricing turns token counts into money.
//
// It holds the arithmetic and nothing else: where the rates come from — a
// published catalogue, a file the operator wrote, a table compiled in — is
// the caller's business, and each incarnation answers it differently.
//
// The rates are a convenience, not an invoice: providers change them, and a
// figure that is a few percent off still answers the question actually
// being asked — whether a session cost cents or euros.
package pricing

import (
	"fmt"
	"strings"
)

// Rate is what one model charges, in euros per million tokens.
//
// Cached is zero for providers that give no discount on a cache hit. OVH is
// one of them, so a rate table with three tiers would misreport it.
type Rate struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
	Cached float64 `yaml:"cached"`
}

// Cost returns what a run of these token counts costs.
//
// input is the provider's total prompt tokens, cached the part of it served
// from the prefix cache — so the fresh portion is the difference. A provider
// that reports no cache hits charges everything at the input rate, which is
// what happens naturally when cached is zero.
func (r Rate) Cost(input, cached, output int) float64 {
	fresh := input - cached
	if fresh < 0 {
		fresh = 0
	}
	cachedRate := r.Cached
	if cachedRate == 0 {
		// No discount published means the cache costs full price, not that
		// it is free.
		cachedRate = r.Input
	}
	return (float64(fresh)*r.Input + float64(cached)*cachedRate + float64(output)*r.Output) / 1_000_000
}

// Table maps a model name to its rate.
type Table map[string]Rate

// Cost returns what a run cost, and whether the model's rate is known.
func (t Table) Cost(model string, input, cached, output int) (float64, bool) {
	rate, ok := t[normalize(model)]
	if !ok {
		return 0, false
	}
	return rate.Cost(input, cached, output), true
}

// Set records a rate under the key Cost will look it up by, so a caller
// folding several sources into one table does not have to know how a model
// name is keyed.
func (t Table) Set(model string, r Rate) { t[normalize(model)] = r }

// Get reads a rate under the same key Set writes it by.
func (t Table) Get(model string) (Rate, bool) {
	r, ok := t[normalize(model)]
	return r, ok
}

// normalize makes lookups tolerant of the decorations a server adds — Ollama
// appends ":latest", and operators tag their own variants.
func normalize(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(m, ':'); i > 0 {
		m = m[:i]
	}
	return m
}

// Format renders an amount the way an operator reads it: cents when the
// figure is small, which is where most single runs land.
func Format(eur float64) string {
	if eur == 0 {
		return "—"
	}
	if eur < 0.01 {
		return fmt.Sprintf("%.4f €", eur)
	}
	if eur < 1 {
		return fmt.Sprintf("%.3f €", eur)
	}
	return fmt.Sprintf("%.2f €", eur)
}
