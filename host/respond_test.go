package host

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
)

type scriptedReply struct {
	respond string
	text    string
}

// scriptedModel answers each request with the next reply: a call to respond
// with the given arguments, or plain text.
func scriptedModel(t *testing.T, replies ...scriptedReply) (agent.Provider, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := len(bodies)
		bodies = append(bodies, string(raw))
		mu.Unlock()
		reply := replies[len(replies)-1]
		if n < len(replies) {
			reply = replies[n]
		}
		w.Header().Set("Content-Type", "text/event-stream")
		write := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
		}
		if reply.respond != "" {
			write(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": "r1", "type": "function",
				"function": map[string]any{"name": respondName, "arguments": reply.respond},
			}}}, "finish_reason": "tool_calls"}}})
		} else {
			write(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"content": reply.text}, "finish_reason": "stop"}}})
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return agent.Provider{BaseURL: srv.URL + "/v1", Model: "m"}, &bodies
}

var styleSchema = map[string]any{
	"type":     "object",
	"required": []any{"category", "energy"},
	"properties": map[string]any{
		"category": map[string]any{"type": "string"},
		"energy":   map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
	},
}

func structuredTurn(t *testing.T, replies ...scriptedReply) ([]event, *[]string) {
	t.Helper()
	provider, bodies := scriptedModel(t, replies...)
	srv := start(t, Config{Agent: Agent{Name: "partage"}, Provider: provider, Token: "t"})
	return readStream(t, rpc(t, srv.URL, "t", "message/stream", message("suggest a style", "ctx-style", map[string]any{
		OutputSchemaKey: styleSchema,
	}))), bodies
}

func answerOf(events []event) (map[string]any, string, bool) {
	var data map[string]any
	var text strings.Builder
	var toolCalls bool
	for _, ev := range events {
		if ev.Kind != "artifact-update" {
			continue
		}
		switch ev.Artifact.Name {
		case "answer":
			for _, p := range ev.Artifact.Parts {
				if d, ok := p["data"].(map[string]any); ok {
					data = d
				}
				if s, ok := p["text"].(string); ok {
					text.WriteString(s)
				}
			}
		case "tool-call":
			toolCalls = true
		}
	}
	return data, text.String(), toolCalls
}

func finalState(events []event) string {
	for _, ev := range events {
		if ev.Final {
			return ev.Status.State
		}
	}
	return ""
}

func TestATurnWithAnOutputSchemaAnswersWithDataThatMatchesIt(t *testing.T) {
	events, bodies := structuredTurn(t, scriptedReply{respond: `{"category":"devtools","energy":70}`})

	data, text, toolCalls := answerOf(events)
	if data["category"] != "devtools" || data["energy"] != float64(70) {
		t.Errorf("answer data = %v", data)
	}
	if text != "" || toolCalls {
		t.Errorf("a structured answer carries no text and no respond trace: text %q, tool calls %v", text, toolCalls)
	}
	if finalState(events) != "completed" {
		t.Errorf("state = %q", finalState(events))
	}
	if len(*bodies) != 1 {
		t.Errorf("the turn must end on a valid respond, without another model call; got %d calls", len(*bodies))
	}
	if !strings.Contains((*bodies)[0], `"respond"`) || !strings.Contains((*bodies)[0], "required format") || !strings.Contains((*bodies)[0], `"maximum":100`) {
		t.Errorf("the model was not offered respond and told to use it: %s", (*bodies)[0])
	}
}

func TestAnAnswerOutsideTheSchemaIsSentBackToBeCorrected(t *testing.T) {
	events, bodies := structuredTurn(t,
		scriptedReply{respond: `{"category":"devtools","energy":140}`},
		scriptedReply{respond: `{"category":"devtools","energy":90}`},
	)

	data, _, _ := answerOf(events)
	if data["energy"] != float64(90) || finalState(events) != "completed" {
		t.Fatalf("answer %v, state %q", data, finalState(events))
	}
	if len(*bodies) != 2 || !strings.Contains((*bodies)[1], "does not match the expected format") {
		t.Errorf("the second call must carry why the first answer was refused: %v", *bodies)
	}
}

func TestATurnThatAnswersInTextIsRemindedThenFails(t *testing.T) {
	events, bodies := structuredTurn(t, scriptedReply{text: `{"category":"devtools",}`})

	if finalState(events) != "failed" {
		t.Fatalf("state = %q", finalState(events))
	}
	if len(*bodies) != 1+maxRespondReminders {
		t.Errorf("model calls = %d, want the first and %d reminders", len(*bodies), maxRespondReminders)
	}
	if !strings.Contains((*bodies)[1], "You did not call respond") {
		t.Errorf("the reminder was not sent: %s", (*bodies)[1])
	}
	if data, text, _ := answerOf(events); data != nil || text != "" {
		t.Errorf("a failed structured turn returns no answer: %v %q", data, text)
	}
}

func TestATextAnswerAfterAReminderStillEndsWithTheData(t *testing.T) {
	events, _ := structuredTurn(t,
		scriptedReply{text: "Voici le style."},
		scriptedReply{respond: `{"category":"devtools","energy":50}`},
	)
	data, _, _ := answerOf(events)
	if data["energy"] != float64(50) || finalState(events) != "completed" {
		t.Errorf("answer %v, state %q", data, finalState(events))
	}
}

func TestAnInvalidSchemaFailsTheTurnAtOnce(t *testing.T) {
	provider, bodies := scriptedModel(t, scriptedReply{text: "x"})
	srv := start(t, Config{Agent: Agent{Name: "partage"}, Provider: provider, Token: "t"})
	events := readStream(t, rpc(t, srv.URL, "t", "message/stream", message("x", "ctx-bad", map[string]any{
		OutputSchemaKey: map[string]any{"type": "not-a-type"},
	})))
	if finalState(events) != "failed" || len(*bodies) != 0 {
		t.Errorf("state %q after %d model calls", finalState(events), len(*bodies))
	}
}

func TestATurnWithoutASchemaStillAnswersInText(t *testing.T) {
	provider, bodies := scriptedModel(t, scriptedReply{text: "Bonjour."})
	srv := start(t, Config{Agent: Agent{Name: "partage"}, Provider: provider, Token: "t"})
	events := readStream(t, rpc(t, srv.URL, "t", "message/stream", message("x", "ctx-text", nil)))
	if _, text, _ := answerOf(events); text != "Bonjour." {
		t.Errorf("text = %q", text)
	}
	if strings.Contains((*bodies)[0], `"respond"`) {
		t.Errorf("respond is offered only when a schema is asked")
	}
}
