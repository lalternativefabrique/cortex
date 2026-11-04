package host

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/profile"
)

// MaxSignalsPerCall bounds one report of gestures.
const MaxSignalsPerCall = 50

// SignalDTO is one gesture an app reports for a person: what they said,
// edited, sent, kept or dismissed, outside any turn.
type SignalDTO struct {
	ID           string    `json:"id,omitempty"`
	Kind         string    `json:"kind"`
	Weight       int       `json:"weight,omitempty"`
	Target       string    `json:"target,omitempty"`
	Content      string    `json:"content,omitempty"`
	Conversation string    `json:"conversation,omitempty"`
	At           time.Time `json:"at,omitempty"`
}

type SignalsRequest struct {
	Subject string      `json:"subject"`
	Signals []SignalDTO `json:"signals"`
}

type FactDTO struct {
	ID     string    `json:"id"`
	Facet  string    `json:"facet"`
	Text   string    `json:"text"`
	Source string    `json:"source"`
	Since  time.Time `json:"since"`
}

type ProfileDTO struct {
	Subject string    `json:"subject"`
	Facts   []FactDTO `json:"facts"`
}

func (h *Host) profileRoutes(mux *http.ServeMux) {
	mux.Handle("POST /profile/signals", h.authorized(http.HandlerFunc(h.reportSignals)))
	mux.Handle("GET /profile", h.authorized(http.HandlerFunc(h.serveProfile)))
	mux.Handle("DELETE /profile/facts/{id}", h.authorized(http.HandlerFunc(h.retractFact)))
}

// reportSignals records the gestures an app saw and learns from them in the
// background, as a turn does once answered.
func (h *Host) reportSignals(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Profile == nil {
		http.Error(w, "this agent keeps no profile", http.StatusNotFound)
		return
	}
	var req SignalsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "a JSON body is required", http.StatusBadRequest)
		return
	}
	subject := strings.TrimSpace(req.Subject)
	if subject == "" || len(req.Signals) == 0 || len(req.Signals) > MaxSignalsPerCall {
		http.Error(w, "a subject and 1 to 50 signals are required", http.StatusBadRequest)
		return
	}
	person := profile.Scope{Subject: subject}
	now := time.Now().UTC()
	for _, s := range req.Signals {
		kind := strings.TrimSpace(s.Kind)
		if kind == "" {
			http.Error(w, "every signal needs a kind", http.StatusBadRequest)
			return
		}
		at := s.At
		if at.IsZero() || at.After(now) {
			at = now
		}
		err := h.cfg.Profile.Observe(r.Context(), profile.Signal{
			Scope: person, ID: s.ID, Agent: h.cfg.Agent.Name, Conversation: s.Conversation, Kind: kind,
			Weight: s.Weight, Target: profile.Clip(s.Target, profile.MaxSignalBytes),
			Content: profile.Clip(s.Content, profile.MaxSignalBytes), At: at,
		})
		if err != nil {
			http.Error(w, "the signal was not recorded", http.StatusInternalServerError)
			return
		}
	}
	client, err := agent.NewClient(h.cfg.Provider)
	if err == nil {
		client, err = agent.WithFallbacks(client, h.cfg.Fallbacks...)
	}
	if err != nil {
		slog.WarnContext(r.Context(), "cortex: profile not learned from signals", "error", err)
	} else {
		h.learn(r.Context(), slog.Default(), client, &person)
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Host) serveProfile(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Profile == nil {
		http.Error(w, "this agent keeps no profile", http.StatusNotFound)
		return
	}
	subject := strings.TrimSpace(r.URL.Query().Get("subject"))
	if subject == "" {
		http.Error(w, "a subject is required", http.StatusBadRequest)
		return
	}
	facts, err := h.cfg.Profile.Facts(r.Context(), profile.Scope{Subject: subject})
	if err != nil {
		http.Error(w, "the profile cannot be read", http.StatusInternalServerError)
		return
	}
	out := ProfileDTO{Subject: subject, Facts: make([]FactDTO, 0, len(facts))}
	for _, f := range facts {
		out.Facts = append(out.Facts, FactDTO{ID: f.ID, Facet: string(f.Facet), Text: f.Text, Source: string(f.Source), Since: f.Since})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// retractFact closes one line the person or the app says is wrong; it stays
// in the history as no longer true.
func (h *Host) retractFact(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Profile == nil {
		http.Error(w, "this agent keeps no profile", http.StatusNotFound)
		return
	}
	subject := strings.TrimSpace(r.URL.Query().Get("subject"))
	if subject == "" {
		http.Error(w, "a subject is required", http.StatusBadRequest)
		return
	}
	if err := h.cfg.Profile.Retract(r.Context(), profile.Scope{Subject: subject}, r.PathValue("id"), time.Now().UTC()); err != nil {
		http.Error(w, "the fact was not retracted", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
