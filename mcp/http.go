package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// httpTransport is JSON-RPC 2.0 over one POST per call, matching
// digstack/synthiz's apps/core/mcpserver: stateless, no session, no
// transport-level auth, always HTTP 200 (a non-200 means the transport
// itself broke, not that the RPC failed — an RPC failure is the "error"
// field in an otherwise-200 body).
type httpTransport struct {
	url  string
	http *http.Client
}

func newHTTPTransport(sc ServerConfig) *httpTransport {
	return &httpTransport{
		url:  sc.URL,
		http: &http.Client{Timeout: sc.Timeout},
	}
}

func (t *httpTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := int64(1) // one request per POST: nothing else to disambiguate against.
	resp, err := t.doCall(ctx, request{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// notify sends a request with no id and does not read the response body — a
// notification (notifications/initialized) gets a bare 200 with no content
// (synthiz's handler answers with c.NoContent), so decoding it as a
// JSON-RPC response the way call does would fail on every empty body.
func (t *httpTransport) notify(ctx context.Context, method string, params any) error {
	httpResp, err := t.send(ctx, request{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	// A notification has no response to send: the spec answers it 202
	// Accepted, and some servers 204.
	if httpResp.StatusCode == http.StatusAccepted || httpResp.StatusCode == http.StatusNoContent {
		return nil
	}
	return checkStatus(method, httpResp)
}

// call sends one JSON-RPC request expecting a reply and decodes it.
func (t *httpTransport) doCall(ctx context.Context, req request) (response, error) {
	httpResp, err := t.send(ctx, req)
	if err != nil {
		return response{}, err
	}
	defer httpResp.Body.Close()

	if err := checkStatus(req.Method, httpResp); err != nil {
		return response{}, err
	}

	var resp response
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return response{}, fmt.Errorf("decode %s response: %w", req.Method, err)
	}
	return resp, nil
}

// send POSTs one JSON-RPC request and returns the raw HTTP response, body
// unread — call and notify differ only in what they do with it.
func (t *httpTransport) send(ctx context.Context, req request) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", req.Method, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", req.Method, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range headersFrom(ctx) {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := t.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", req.Method, err)
	}
	return httpResp, nil
}

// checkStatus reports a transport-level failure. synthiz's server always
// answers 200 for an RPC that reached it, error or not, so any other status
// means the transport itself broke — not that the RPC failed.
func checkStatus(method string, resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("%s: unexpected status %d: %s", method, resp.StatusCode, snippet)
}

func (t *httpTransport) close() error { return nil }
