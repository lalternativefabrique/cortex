// Package lalter keeps a cortex host's agent memory in lalter (lalter ADR
// 0012): a recall.Store over lalter's /api/v1/recall API.
//
// The host's app key names the tenant; the subject of every call is the
// host's own id for the person, sent as the end user lalter scopes to.
package lalter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lalternative/packages/go/cortex/recall"
)

const endUserHeader = "X-End-User-Id"

type Config struct {
	// BaseURL is lalter's API root, e.g. https://api.lalter.fr/api/v1.
	BaseURL string
	// AppKey holds the recall scope.
	AppKey string
	// Client nil uses one with a 15-second timeout.
	Client *http.Client
}

type Store struct {
	base   string
	key    string
	client *http.Client
}

var _ recall.Store = (*Store)(nil)

func New(cfg Config) *Store {
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Store{base: strings.TrimRight(cfg.BaseURL, "/"), key: cfg.AppKey, client: client}
}

type entryBody struct {
	Agent        string `json:"agent"`
	Conversation string `json:"conversation"`
	Message      string `json:"message"`
	Kind         string `json:"kind"`
	Role         string `json:"role,omitempty"`
	Content      string `json:"content"`
	At           string `json:"at,omitempty"`
}

type searchBody struct {
	Agent string `json:"agent"`
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type searchResult struct {
	Entries []struct {
		Conversation string `json:"conversation"`
		Message      string `json:"message"`
		Kind         string `json:"kind"`
		Role         string `json:"role"`
		Content      string `json:"content"`
		At           string `json:"at"`
	} `json:"entries"`
}

func (s *Store) Remember(ctx context.Context, e recall.Entry) error {
	body := entryBody{
		Agent: e.Agent, Conversation: e.Conversation, Message: e.Message,
		Kind: string(e.Kind), Role: e.Role, Content: e.Content,
	}
	if !e.At.IsZero() {
		body.At = e.At.UTC().Format(time.RFC3339)
	}
	return s.do(ctx, http.MethodPost, "/recall/entries", e.Subject, body, nil)
}

func (s *Store) Recall(ctx context.Context, scope recall.Scope, query string, limit int) ([]recall.Entry, error) {
	var res searchResult
	if err := s.do(ctx, http.MethodPost, "/recall/search", scope.Subject, searchBody{Agent: scope.Agent, Query: query, Limit: limit}, &res); err != nil {
		return nil, err
	}
	out := make([]recall.Entry, 0, len(res.Entries))
	for _, r := range res.Entries {
		at, _ := time.Parse(time.RFC3339, r.At)
		out = append(out, recall.Entry{
			Scope: scope, Conversation: r.Conversation, Message: r.Message,
			Kind: recall.Kind(r.Kind), Role: r.Role, Content: r.Content, At: at,
		})
	}
	return out, nil
}

func (s *Store) Forget(ctx context.Context, scope recall.Scope, conversation string) error {
	return s.do(ctx, http.MethodDelete, "/recall/conversations/"+url.PathEscape(conversation), scope.Subject, nil, nil)
}

func (s *Store) do(ctx context.Context, method, path, subject string, in, out any) error {
	if strings.TrimSpace(subject) == "" {
		return fmt.Errorf("recall: a subject is required")
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set(endUserHeader, subject)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("recall %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("recall %s %s: lalter answered %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
