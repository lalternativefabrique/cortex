package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeClient struct {
	answer string
	err    error
	calls  int
	// deltas, when set, are streamed before err is returned.
	deltas []string
}

func (c *fakeClient) Complete(context.Context, CompletionRequest) (CompletionResponse, error) {
	c.calls++
	if c.err != nil {
		return CompletionResponse{}, c.err
	}
	return CompletionResponse{Text: c.answer}, nil
}

func (c *fakeClient) CompleteStream(_ context.Context, _ CompletionRequest, onDelta func(string)) (CompletionResponse, error) {
	c.calls++
	for _, d := range c.deltas {
		onDelta(d)
	}
	if c.err != nil {
		return CompletionResponse{}, c.err
	}
	return CompletionResponse{Text: c.answer}, nil
}

func withClients(primary Client, others ...Client) *fallbackClient {
	f := &fallbackClient{clients: []Client{primary}, labels: []string{"primary"}}
	for i, c := range others {
		f.clients = append(f.clients, c)
		f.labels = append(f.labels, "fallback-"+string(rune('a'+i)))
	}
	return f
}

func TestTheNextProviderAnswersWhenTheFirstFails(t *testing.T) {
	down := &fakeClient{err: &APIError{StatusCode: http.StatusBadGateway}}
	up := &fakeClient{answer: "Bonjour."}
	f := withClients(down, up)

	resp, err := f.Complete(context.Background(), CompletionRequest{})
	if err != nil || resp.Text != "Bonjour." || down.calls != 1 || up.calls != 1 {
		t.Fatalf("resp = %+v, err = %v, calls = %d/%d", resp, err, down.calls, up.calls)
	}
}

func TestEveryFailureIsNamedWhenNoProviderAnswers(t *testing.T) {
	f := withClients(&fakeClient{err: errors.New("timeout")}, &fakeClient{err: &APIError{StatusCode: 401}})
	_, err := f.Complete(context.Background(), CompletionRequest{})
	if err == nil || !strings.Contains(err.Error(), "primary: timeout") || !strings.Contains(err.Error(), "fallback-a:") {
		t.Fatalf("err = %v", err)
	}
}

func TestACancelledCallIsNotAskedElsewhere(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	up := &fakeClient{answer: "late"}
	f := withClients(&fakeClient{err: context.Canceled}, up)
	if _, err := f.Complete(ctx, CompletionRequest{}); !errors.Is(err, context.Canceled) || up.calls != 0 {
		t.Fatalf("err = %v, fallback calls = %d", err, up.calls)
	}
}

func TestAnAnswerAlreadyStreamedIsNotRestartedElsewhere(t *testing.T) {
	broken := &fakeClient{deltas: []string{"Voici "}, err: errors.New("connection reset")}
	up := &fakeClient{answer: "Voici le mail."}
	f := withClients(broken, up)
	var got strings.Builder
	_, err := f.CompleteStream(context.Background(), CompletionRequest{}, func(s string) { got.WriteString(s) })
	if err == nil || up.calls != 0 || got.String() != "Voici " {
		t.Fatalf("err = %v, fallback calls = %d, streamed %q", err, up.calls, got.String())
	}

	silent := &fakeClient{err: errors.New("connection reset")}
	f = withClients(silent, up)
	got.Reset()
	resp, err := f.CompleteStream(context.Background(), CompletionRequest{}, func(s string) { got.WriteString(s) })
	if err != nil || resp.Text != "Voici le mail." || up.calls != 1 {
		t.Fatalf("resp = %+v, err = %v, fallback calls = %d", resp, err, up.calls)
	}
}

func TestWithFallbacksBuildsRealClientsAndLabelsThem(t *testing.T) {
	primary := &fakeClient{answer: "ok"}
	if c, err := WithFallbacks(primary); err != nil || c != primary {
		t.Fatalf("no fallback must return the primary itself: %v, %v", c, err)
	}
	if _, err := WithFallbacks(primary, Provider{Model: "m"}); err == nil {
		t.Fatal("a fallback without a base URL was accepted")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	defer srv.Close()
	c, err := WithFallbacks(primary, Provider{BaseURL: srv.URL + "/v1", Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if labels := c.(*fallbackClient).labels; labels[1] != "m@"+strings.TrimPrefix(srv.URL, "http://") {
		t.Fatalf("labels = %v", labels)
	}
}
