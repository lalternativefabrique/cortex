package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

type fakeWorkState struct{ repo RepoState }

func (f fakeWorkState) State(context.Context) (RepoState, error) { return f.repo, nil }

const writtenSections = headingCode + "\npkg/foo/foo.go: Parse drops the last field.\n\n" +
	headingDecisions + "\nKeep the parser; fix the loop bound.\n\n" +
	headingFailures + "\nNone.\n\n" +
	headingVerification + "\n--- FAIL: TestParse\n\n" +
	headingRemaining + "\nNext: change the bound in Parse.\n\n" +
	headingQuestions + "\nNone.\n"

func withMessage(msgs []Message, at int, m Message) []Message {
	out := append([]Message(nil), msgs[:at]...)
	out = append(out, m)
	return append(out, msgs[at:]...)
}

func TestAnInstructionGivenMidwaySurvivesCompactionVerbatim(t *testing.T) {
	instruction := "Do not touch pkg/bar.\n  Keep the public API exactly as it is."
	client := &scriptedClient{responses: []CompletionResponse{{Text: writtenSections}}}
	c := &SummaryCompactor{Client: client, KeepRecent: 4}

	out, _, _, err := c.Compact(context.Background(), "sys", withMessage(longHistory(10), 5, Message{Role: RoleUser, Content: instruction}))
	if err != nil {
		t.Fatal(err)
	}
	h := out[0].Handoff
	if h == nil || !reflect.DeepEqual(h.Instructions, []string{instruction}) {
		t.Fatalf("instructions = %+v, want the one given midway, verbatim", h)
	}
	if !strings.Contains(out[0].Content, instruction) {
		t.Fatalf("the rendered handoff lacks the instruction verbatim:\n%s", out[0].Content)
	}
	if h.Remaining != "Next: change the bound in Parse." || h.Verification != "--- FAIL: TestParse" {
		t.Fatalf("the model's sections were not read: %+v", h)
	}
}

func TestTheTaskSurvivesCompactionBehindAWorkspaceSnapshot(t *testing.T) {
	task := "fix the failing test in pkg/foo"
	history := append([]Message{
		{Role: RoleUser, Content: "Workspace: /repo\nBranch: main\n"},
		{Role: RoleAssistant, Content: "Noted. What would you like to do?"},
		{Role: RoleUser, Content: task, Task: true},
	}, longHistory(10)[1:]...)
	client := &scriptedClient{responses: []CompletionResponse{{Text: writtenSections}}}
	c := &SummaryCompactor{Client: client, KeepRecent: 4}

	out, _, _, err := c.Compact(context.Background(), "sys", history)
	if err != nil {
		t.Fatal(err)
	}
	h := out[0].Handoff
	if h == nil || h.Task != task {
		t.Fatalf("task = %+v, want %q", h, task)
	}
	if len(h.Instructions) != 0 {
		t.Fatalf("the workspace snapshot was taken for an instruction: %q", h.Instructions)
	}

	again, _, _, err := c.Compact(context.Background(), "sys", append(out, longHistory(10)[1:]...))
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Handoff == nil || again[0].Handoff.Task != task {
		t.Fatalf("the task did not survive a second compaction: %+v", again[0].Handoff)
	}
}

func TestTheHandoffCarriesTheRepositoryState(t *testing.T) {
	repo := RepoState{
		Branch:   "feat/parse",
		Base:     "origin/main",
		Commits:  []string{"abc1234 fix(foo): bound the loop"},
		Status:   " M pkg/foo/foo.go",
		Diffstat: " pkg/foo/foo.go | 2 +-",
	}
	client := &scriptedClient{responses: []CompletionResponse{{Text: writtenSections}}}
	c := &SummaryCompactor{Client: client, KeepRecent: 4, WorkState: fakeWorkState{repo}}

	out, _, _, err := c.Compact(context.Background(), "the agent's own prompt", longHistory(10))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"feat/parse", "origin/main", "abc1234 fix(foo): bound the loop", " M pkg/foo/foo.go", "pkg/foo/foo.go | 2 +-", handoffClosing} {
		if !strings.Contains(out[0].Content, want) {
			t.Errorf("the handoff lacks %q", want)
		}
	}
	if !strings.Contains(client.calls[0].Messages[0].Content, "the agent's own prompt") {
		t.Error("the summariser was not given the agent's system prompt")
	}
}

func TestATestFailureReachesTheSummariserLongerThanOtherOutput(t *testing.T) {
	failure := "--- FAIL: TestParse\n" + strings.Repeat("assertion detail\n", 200) + "FAIL pkg/foo"
	other := strings.Repeat("listing line\n", 200)
	history := []Message{
		{Role: RoleUser, Content: "task", Task: true},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "bash", Arguments: json.RawMessage(`{"command":"go test ./..."}`)}}},
		{Role: RoleTool, ToolCallID: "a", Content: failure},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "b", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*"}`)}}},
		{Role: RoleTool, ToolCallID: "b", Content: other},
		{Role: RoleAssistant, Content: "looking"},
	}
	client := &scriptedClient{responses: []CompletionResponse{{Text: writtenSections}}}
	if _, _, _, err := (&SummaryCompactor{Client: client, KeepRecent: 1}).Compact(context.Background(), "", history); err != nil {
		t.Fatal(err)
	}
	sent := client.calls[0].Messages[0].Content
	if !strings.Contains(sent, "FAIL pkg/foo") || !strings.Contains(sent, strings.Repeat("assertion detail\n", 100)) {
		t.Error("the test failure was cut as short as any other output")
	}
	if strings.Contains(sent, strings.Repeat("listing line\n", 150)) {
		t.Error("ordinary output was kept past its limit")
	}
}

func TestAHandoffReadsBackWhatItRendered(t *testing.T) {
	h := Handoff{
		Task:         "fix the failing test\n\nin pkg/foo",
		Instructions: []string{"first, verbatim", "second\n- with a list"},
		Code:         "pkg/foo/foo.go",
		Decisions:    "Keep the parser.",
		Failures:     "go test: undefined: Bound",
		Verification: "--- FAIL: TestParse",
		Remaining:    "Next: change the bound.",
		Questions:    "Does pkg/bar depend on it?",
	}
	got := ParseHandoff(h.Render())
	if !reflect.DeepEqual(got, h) {
		t.Fatalf("ParseHandoff(Render()) =\n%+v\nwant\n%+v", got, h)
	}
	if plain := ParseHandoff("just do the thing"); plain.Task != "just do the thing" {
		t.Fatalf("a document without headings was not taken as the task: %+v", plain)
	}
}

// wireClient answers the loop with tool calls until it has made calls of
// them, and the summariser with the handoff's sections. It records every
// request, which is what the provider would have received.
type wireClient struct {
	mu    sync.Mutex
	loop  int
	calls int
	sent  []CompletionRequest
}

func (c *wireClient) Complete(_ context.Context, req CompletionRequest) (CompletionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, req)
	if req.System == HandoffInstruction {
		return CompletionResponse{Text: writtenSections}, nil
	}
	c.loop++
	if c.loop > c.calls {
		return CompletionResponse{Text: "done"}, nil
	}
	return CompletionResponse{ToolCalls: []ToolCall{{ID: "c" + strings.Repeat("x", c.loop), Name: "probe", Arguments: json.RawMessage(`{}`)}}}, nil
}

type handoffSink struct {
	NopCallback
	mu       sync.Mutex
	handoffs []Handoff
}

func (s *handoffSink) OnHandoff(h Handoff) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handoffs = append(s.handoffs, h)
}

func TestNoTwoUserMessagesFollowEachOtherOnTheWire(t *testing.T) {
	tool := &fakeTool{name: "probe", result: strings.Repeat("verbose tool output\n", 300)}
	client := &wireClient{calls: 8}
	var steered sync.Once
	sink := &handoffSink{}
	r, err := NewRunner(Config{
		Client:        client,
		Tools:         []Tool{tool},
		MaxSteps:      12,
		StallAfter:    -1,
		ContextWindow: 4000,
		Compactor:     &SummaryCompactor{Client: client, KeepRecent: 3},
		WorkState:     fakeWorkState{RepoState{Branch: "feat/x"}},
		Callback:      sink,
		Steer: func() []string {
			var added []string
			if client.loop == 4 {
				steered.Do(func() { added = []string{"also keep the old flag"} })
			}
			return added
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{
		{Role: RoleUser, Content: "Workspace: /repo"},
		{Role: RoleAssistant, Content: "Noted. What would you like to do?"},
		{Role: RoleUser, Content: "rename the flag", Task: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Compactions == 0 {
		t.Fatal("the history was never compacted")
	}
	for i, req := range client.sent {
		for j := 1; j < len(req.Messages); j++ {
			if wireRole(req.Messages[j].Role) == RoleUser && wireRole(req.Messages[j-1].Role) == RoleUser {
				t.Fatalf("request %d sends two user messages in a row at %d", i, j)
			}
		}
	}
	h, ok := HandoffOf(res.Messages)
	if !ok {
		t.Fatal("Result.Messages lost the compaction")
	}
	if h.Task != "rename the flag" || h.Repo == nil || h.Repo.Branch != "feat/x" {
		t.Fatalf("handoff = %+v", h)
	}
	kept := slices.Contains(h.Instructions, "also keep the old flag")
	for _, m := range res.Messages {
		kept = kept || m.Content == "also keep the old flag"
	}
	if !kept {
		t.Fatal("the instruction added mid-run was lost")
	}
	if len(sink.handoffs) != res.Compactions {
		t.Fatalf("OnHandoff heard %d handoffs for %d compactions", len(sink.handoffs), res.Compactions)
	}
	if res.Messages[len(res.Messages)-1].Content != "done" {
		t.Fatal("Result.Messages does not end on the answer")
	}
}
