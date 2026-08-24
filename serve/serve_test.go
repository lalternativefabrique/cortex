package serve

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
)

func newServer(t *testing.T, token string) http.Handler {
	t.Helper()
	s, err := New(Config{Root: t.TempDir(), Token: token})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /turn", s.handleTurn)
	mux.HandleFunc("GET /workspace", s.handleWorkspace)
	return s.authenticated(mux)
}

func TestATurnWithoutTheTokenIsRefused(t *testing.T) {
	// Loopback is not a permission: every process on this machine can reach
	// this, a page in a browser included, and it runs shell commands.
	h := newServer(t, "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/turn", strings.NewReader("{}")))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestTheWrongTokenIsRefused(t *testing.T) {
	h := newServer(t, "secret")
	req := httptest.NewRequest(http.MethodGet, "/workspace", nil)
	req.Header.Set("Authorization", "Bearer not-it")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestTheWorkspaceSaysWhatIsOnOffer(t *testing.T) {
	// A window has to be able to say which repository it is pointed at before
	// anything is asked of it.
	h := newServer(t, "secret")
	req := httptest.NewRequest(http.MethodGet, "/workspace", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"root"`) {
		t.Fatal("the workspace did not name the directory it serves")
	}
}

func TestATurnWithNoMessagesIsRefused(t *testing.T) {
	h := newServer(t, "secret")
	req := httptest.NewRequest(http.MethodPost, "/turn", strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestARootIsRequired(t *testing.T) {
	// Without one every tool would resolve against the process's working
	// directory, which is whatever the launcher happened to be in.
	if _, err := New(Config{}); err == nil {
		t.Fatal("a server with no workspace was accepted")
	}
}

func TestTheAgentGetsTheToolsThatNeedThisMachine(t *testing.T) {
	// The point of running here rather than on the server: reading files,
	// searching them, changing them, and running commands in the environment
	// they belong to.
	s, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range s.tools {
		got[tool.Name()] = true
	}
	for _, want := range []string{"read", "grep", "glob", "edit", "write", "bash"} {
		if !got[want] {
			t.Errorf("%s is missing; the window would have to ask the server for it", want)
		}
	}
}

func TestTheCallerPromptKeepsTheWorkspaceDescription(t *testing.T) {
	// The caller writes the conversation's own prompt and knows nothing about
	// where it runs; the workspace is what only this side can describe.
	s, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	prompt := s.system("You are a careful assistant.")
	if !strings.Contains(prompt, "You are a careful assistant.") {
		t.Fatal("the caller's instructions were dropped")
	}
	if !strings.Contains(prompt, "Workspace:") {
		t.Fatal("the agent was not told where it is working")
	}
}

func TestMessagesKeepTheirRoles(t *testing.T) {
	got := toAgentMessages([]Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "system", Content: "three"},
		// An unknown role reads as the person speaking: a turn attributed to
		// the model that the model did not say would rewrite the history.
		{Role: "wat", Content: "four"},
	})
	want := []agent.Role{agent.RoleUser, agent.RoleAssistant, agent.RoleSystem, agent.RoleUser}
	for i, w := range want {
		if got[i].Role != w {
			t.Errorf("message %d is %q, want %q", i, got[i].Role, w)
		}
	}
}

func TestHealthIsServedWithoutTheToken(t *testing.T) {
	// A readiness probe carries no token, and the kubelet counts only 2xx-3xx
	// as healthy: behind the token this endpoint could never pass.
	s, err := New(Config{Root: t.TempDir(), Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go s.Serve(ln)

	res, err := http.Get("http://" + ln.Addr().String() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	// The probe learns the process is up, not where it is working.
	if strings.Contains(string(body), "root") {
		t.Fatalf("body leaks the workspace path to an unauthenticated caller: %s", body)
	}

	// The token still guards everything else.
	res2, err := http.Get("http://" + ln.Addr().String() + "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/workspace status = %d, want 401", res2.StatusCode)
	}
}
