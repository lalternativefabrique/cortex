package serve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func postEvent(t *testing.T, base, token, body string) (map[string]any, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+eventsPath, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", eventsPath, err)
	}
	defer res.Body.Close()

	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out, res.StatusCode
}

const deployFailed = `{
  "specversion": "1.0",
  "id": "e-1",
  "source": "/skalpai/deploy",
  "type": "app.deploy.failed",
  "subject": "lalter-core",
  "data": {"reason": "image pull backoff"}
}`

// A webhook fires and is gone: it gets the ids to follow the run by, not the
// run itself held open on its connection.
func TestEventsAcceptAndAnswerImmediately(t *testing.T) {
	base := a2aServer(t, "looked into it")

	out, status := postEvent(t, base, "secret", deployFailed)
	if status != http.StatusAccepted {
		t.Fatalf("status is %d, want 202: %v", status, out)
	}
	if out["contextId"] == "" || out["contextId"] == nil {
		t.Errorf("no contextId in %v — a producer cannot continue the conversation", out)
	}
	if out["taskId"] == "" || out["taskId"] == nil {
		t.Errorf("no taskId in %v — the run cannot be followed", out)
	}
}

// An event naming a context continues that conversation, which is what makes
// a second webhook a follow-up rather than a fresh start.
func TestEventsUseTheNamedContext(t *testing.T) {
	base := a2aServer(t, "noted")

	body := strings.Replace(deployFailed, `"type": "app.deploy.failed",`,
		`"type": "app.deploy.failed", "contextid": "ctx-42",`, 1)
	out, status := postEvent(t, base, "secret", body)
	if status != http.StatusAccepted {
		t.Fatalf("status is %d, want 202: %v", status, out)
	}
	if out["contextId"] != "ctx-42" {
		t.Errorf("contextId is %v, want ctx-42", out["contextId"])
	}
}

// An agent woken by an event it cannot attribute has no way to judge what it
// is being asked, so a malformed envelope is refused rather than run.
func TestEventsRejectAMalformedEnvelope(t *testing.T) {
	base := a2aServer(t, "never reached")

	for _, missing := range []string{"specversion", "id", "source", "type"} {
		var event map[string]any
		if err := json.Unmarshal([]byte(deployFailed), &event); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		delete(event, missing)
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		out, status := postEvent(t, base, "secret", string(body))
		if status != http.StatusBadRequest {
			t.Errorf("an event with no %s got status %d, want 400: %v", missing, status, out)
		}
	}
}

func TestEventsRejectNonJSON(t *testing.T) {
	base := a2aServer(t, "never reached")

	_, status := postEvent(t, base, "secret", "not json at all")
	if status != http.StatusBadRequest {
		t.Errorf("status is %d, want 400", status)
	}
}

// Starting an agent run is not less sensitive because the caller is a
// machine.
func TestEventsRequireTheToken(t *testing.T) {
	base := a2aServer(t, "never reached")

	_, status := postEvent(t, base, "", deployFailed)
	if status != http.StatusUnauthorized {
		t.Errorf("status is %d, want 401", status)
	}
}

// The agent is told what arrived and from where, and handed the payload to
// judge for itself.
func TestEventPromptCarriesTheEnvelopeAndData(t *testing.T) {
	var event cloudEvent
	if err := json.NewDecoder(bytes.NewReader([]byte(deployFailed))).Decode(&event); err != nil {
		t.Fatalf("decode: %v", err)
	}

	prompt := event.prompt()
	for _, want := range []string{"app.deploy.failed", "/skalpai/deploy", "lalter-core", "image pull backoff"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, prompt)
		}
	}
}
