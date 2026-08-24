package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func taskServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s, err := New(Config{WorkDir: t.TempDir(), Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /turn", s.handleTurn)
	mux.HandleFunc("POST /api/v1/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}/steps", s.handleTaskSteps)
	return s, s.authenticated(mux)
}

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestATaskWithoutARepositoryIsRefused(t *testing.T) {
	// Queuing it would spend a goroutine and a temp directory to fail on
	// something this side already knows.
	_, h := taskServer(t)
	rec := post(h, "/api/v1/tasks", `{"kind":"fix","prompt":"fix it"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestATaskWithoutAPromptIsRefused(t *testing.T) {
	_, h := taskServer(t)
	rec := post(h, "/api/v1/tasks", `{"kind":"fix","repo_url":"https://example.test/r.git"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestTheCloneURLNeverComesBack(t *testing.T) {
	// It carries the caller's token. A response that echoes it hands the
	// credential to everything that logs a response body.
	_, h := taskServer(t)
	rec := post(h, "/api/v1/tasks",
		`{"kind":"fix","prompt":"fix it","repo_url":"https://x-access-token:SECRET@github.com/acme/app.git"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatal("the response carried the token the URL was given")
	}
}

func TestAnUnknownKindIsTreatedAsAFix(t *testing.T) {
	// Only diagnose is special — it is the one that gets no way to write.
	// Anything else defaulting to read-only would silently answer a request
	// to change something by changing nothing.
	_, h := taskServer(t)
	rec := post(h, "/api/v1/tasks",
		`{"kind":"refactor","prompt":"fix it","repo_url":"https://example.test/r.git"}`)

	var task Task
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if task.Kind != "fix" {
		t.Fatalf("kind = %q, want fix", task.Kind)
	}
}

func TestADiagnoseRunHasNoWayToChangeAnything(t *testing.T) {
	// Not by instruction: a tool it does not have is a tool it cannot misuse.
	got := map[string]bool{}
	for _, tool := range taskTools(t.TempDir(), "diagnose") {
		got[tool.Name()] = true
	}
	for _, forbidden := range []string{"edit", "write", "bash"} {
		if got[forbidden] {
			t.Errorf("a diagnose run was given %s", forbidden)
		}
	}
	for _, want := range []string{"read", "grep", "glob"} {
		if !got[want] {
			t.Errorf("a diagnose run cannot %s, so it cannot cite anything", want)
		}
	}
}

func TestAFixRunCanChangeAndVerify(t *testing.T) {
	got := map[string]bool{}
	for _, tool := range taskTools(t.TempDir(), "fix") {
		got[tool.Name()] = true
	}
	for _, want := range []string{"read", "grep", "glob", "edit", "write", "bash"} {
		if !got[want] {
			t.Errorf("a fix run cannot %s", want)
		}
	}
}

func TestAnUnknownTaskIsNotFound(t *testing.T) {
	_, h := taskServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/does-not-exist", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestTurnIsRefusedWhenThereIsNoWorkspace(t *testing.T) {
	// A deployment that only runs tasks has no directory to answer about, and
	// every tool would otherwise resolve against whatever the process sits in.
	_, h := taskServer(t)
	rec := post(h, "/turn", `{"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestTheStoreDropsTheOldestRatherThanGrowing(t *testing.T) {
	// A long-lived service answers thousands of tasks, and every one of them
	// holds its diff.
	store := newTaskStore(2)
	for _, id := range []string{"a", "b", "c"} {
		store.add(&Task{ID: id})
	}
	if _, _, ok := store.get("a"); ok {
		t.Fatal("the oldest task was kept past the bound")
	}
	for _, id := range []string{"b", "c"} {
		if _, _, ok := store.get(id); !ok {
			t.Fatalf("%s was dropped while newer than the bound", id)
		}
	}
}

func TestReadingATaskDoesNotHandOutTheLiveStruct(t *testing.T) {
	// The caller polls while the run appends steps. Returning the struct
	// itself would race with every tool call the agent makes.
	store := newTaskStore(4)
	store.add(&Task{ID: "t1", steps: []TaskStep{{Seq: 1, Tool: "read"}}})

	_, steps, _ := store.get("t1")
	steps[0].Tool = "tampered"

	_, again, _ := store.get("t1")
	if again[0].Tool != "read" {
		t.Fatal("a caller mutated the stored task through what it was handed")
	}
}

func TestRedactHidesTheCredentialAndTheAddress(t *testing.T) {
	url := "https://x-access-token:ghp_secret@github.com/acme/app.git"
	out := redact("fatal: could not read from "+url, url)

	if strings.Contains(out, "ghp_secret") {
		t.Fatalf("the token survived redaction: %q", out)
	}
	if strings.Contains(out, "acme/app") {
		t.Fatalf("the repository survived redaction: %q", out)
	}
}

func TestAFailedCloneNeverLogsTheCredential(t *testing.T) {
	// The clone URL carries a token. It is redacted out of the error, and the
	// error is what gets logged -- a log that quotes it publishes it.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	s, err := New(Config{WorkDir: t.TempDir(), Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	task := &Task{
		ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Kind: "fix", Prompt: "p",
		Status: TaskPending, repoURL: "https://x-access-token:ghs_SUPERSECRET@github.com/o/r.git",
	}
	s.tasks.add(task)
	s.runTask(context.Background(), task.ID)

	if strings.Contains(buf.String(), "ghs_SUPERSECRET") {
		t.Fatalf("the clone credential reached the log:\n%s", buf.String())
	}
}
