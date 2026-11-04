package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/lalternativefabrique/cortex/host"
)

// Signal is one gesture of a person the app saw: kind is the app's name for
// it (said, edited, sent, pass, mute), weight is negative when it rejects.
type Signal = host.SignalDTO

// Profile is what the agent learned of one person.
type Profile = host.ProfileDTO

// Observe reports what a person did in the app, outside any turn, so the
// agent learns from it.
func Observe(ctx context.Context, a Agent, subject string, signals []Signal) error {
	body, err := json.Marshal(host.SignalsRequest{Subject: subject, Signals: signals})
	if err != nil {
		return err
	}
	return a.do(ctx, http.MethodPost, "/profile/signals", bytes.NewReader(body), nil)
}

// ReadProfile returns what the agent knows of a person.
func ReadProfile(ctx context.Context, a Agent, subject string) (Profile, error) {
	var p Profile
	err := a.do(ctx, http.MethodGet, "/profile?subject="+url.QueryEscape(subject), nil, &p)
	return p, err
}

// Retract closes one fact of a person's profile that is no longer true.
func Retract(ctx context.Context, a Agent, subject, factID string) error {
	return a.do(ctx, http.MethodDelete, "/profile/facts/"+url.PathEscape(factID)+"?subject="+url.QueryEscape(subject), nil, nil)
}

func (a Agent) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.URL, "/")+path, body)
	if err != nil {
		return err
	}
	for k, v := range a.Header {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpc := a.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	res, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2<<10))
		return fmt.Errorf("cortex %s %s: %d %s", method, path, res.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}
