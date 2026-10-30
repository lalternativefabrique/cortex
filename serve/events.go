package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
)

const eventsPath = "/events"

// cloudEvent is an inbound event in the CloudEvents 1.0 structured form: the
// whole event, envelope and payload, as one JSON object.
//
// Only the attributes cortex acts on are read. An event carries whatever
// extensions its producer chose, and refusing one for a field this does not
// know would make every producer's schema cortex's business.
type cloudEvent struct {
	SpecVersion string          `json:"specversion"`
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	Type        string          `json:"type"`
	Subject     string          `json:"subject,omitempty"`
	Time        string          `json:"time,omitempty"`
	DataSchema  string          `json:"dataschema,omitempty"`
	ContentType string          `json:"datacontenttype,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`

	// ContextID names the conversation this event belongs to. It is a
	// CloudEvents extension, not a core attribute: an event that names none
	// opens its own context, which is what a webhook from a system that knows
	// nothing of cortex will do.
	ContextID string `json:"contextid,omitempty"`
}

// valid reports whether the envelope carries what the spec requires. A
// malformed event is rejected rather than turned into a task: an agent woken
// by an event it cannot attribute has no way to judge what it is being asked.
func (e cloudEvent) valid() error {
	switch {
	case strings.TrimSpace(e.SpecVersion) == "":
		return fmt.Errorf("specversion is required")
	case strings.TrimSpace(e.ID) == "":
		return fmt.Errorf("id is required")
	case strings.TrimSpace(e.Source) == "":
		return fmt.Errorf("source is required")
	case strings.TrimSpace(e.Type) == "":
		return fmt.Errorf("type is required")
	}
	return nil
}

// prompt renders the event as what the agent is asked. An event is not a
// question, so this says plainly what arrived and from where, and hands over
// the payload rather than interpreting it: what to do about a deployment
// having failed is the agent's judgement, not this function's.
func (e cloudEvent) prompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "An event arrived.\n\ntype: %s\nsource: %s", e.Type, e.Source)
	if e.Subject != "" {
		fmt.Fprintf(&b, "\nsubject: %s", e.Subject)
	}
	if e.Time != "" {
		fmt.Fprintf(&b, "\ntime: %s", e.Time)
	}
	if len(e.Data) > 0 {
		fmt.Fprintf(&b, "\n\n%s", strings.TrimSpace(string(e.Data)))
	}
	return b.String()
}

// EventResponse says what became of an inbound event.
type EventResponse struct {
	// TaskID is the task the event opened, which a caller can follow through
	// tasks/get.
	TaskID string `json:"taskId"`
	// ContextID is the conversation it ran in, so a producer sending a second
	// event can continue the same one.
	ContextID string `json:"contextId"`
}

// handleEvents turns an inbound CloudEvent into a task.
//
// A webhook has no conversation and expects no answer: it fires and is gone.
// So this answers as soon as the task is accepted, with the ids to follow it
// by, rather than holding the producer's connection open for the length of a
// run.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	var event cloudEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the event is not JSON"})
		return
	}
	if err := event.valid(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	contextID := strings.TrimSpace(event.ContextID)
	if contextID == "" {
		contextID = a2a.NewContextID()
	}
	taskID := string(a2a.NewTaskID())

	// The run outlives this request, so it takes the server's own context: a
	// producer that hangs up must not cancel the work its event asked for.
	go func() {
		if _, err := s.runTask(context.WithoutCancel(r.Context()), contextID, event.prompt()); err != nil {
			slog.Warn("serve: event task failed",
				"type", event.Type, "source", event.Source, "context", contextID, "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, EventResponse{TaskID: taskID, ContextID: contextID})
}
