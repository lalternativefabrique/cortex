package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// systemPrompt tells the vision model what a coding agent needs from an
// image, which is not what a captioning model volunteers.
//
// The instruction to transcribe text verbatim matters most: a screenshot of
// a stack trace is useless as "an error message is shown", and a paraphrased
// identifier sends the agent looking for a symbol that does not exist.
const systemPrompt = `You look at pictures for a coding agent that cannot see them. It will act on what you write and nothing else, so what you write has to be true of the picture and useful to someone about to change code.

Transcribe text exactly as it appears — error messages, file paths, line numbers, identifiers, URLs, log output, code. Do not paraphrase, correct spelling, or fix what looks like a typo: a corrected identifier sends the agent hunting for a symbol that does not exist. Where text is cut off or unreadable, say so.

Answer as short statements, one per line, each under 25 words. No bullets, no numbering, no preamble.

Good answer, for a terminal screenshot:
` + "`" + `FAIL TestSelfTransferPreservesBalance in ledger_test.go line 39.` + "`" + `
` + "`" + `Message: balance = 140, want 100: a self-transfer must not create or destroy money.` + "`" + `
` + "`" + `The command run was: go test ./...` + "`" + `

Bad answer, for the same screenshot:
` + "`" + `A dark terminal window showing red error text and a test failure summary.` + "`" + `
That describes the screen. The agent learns nothing it can act on.

Claim nothing the picture does not show. If the picture shows nothing you can state that way, say so plainly rather than filling the space.`

// defaultQuestion is used when the caller has nothing specific to ask.
const defaultQuestion = "Describe this image."

type wireRequest struct {
	Model     string        `json:"model"`
	Messages  []wireMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type wireMessage struct {
	Role string `json:"role"`
	// Content is either a plain string or a list of parts. The system turn
	// uses the string form because some servers reject a parts array there.
	Content any `json:"content"`
}

type textPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type imagePart struct {
	Type     string   `json:"type"`
	ImageURL imageURL `json:"image_url"`
}

type imageURL struct {
	URL string `json:"url"`
	// Detail asks for a full-resolution reading. Downscaling is cheaper but
	// loses exactly what a coding agent needs: small text.
	Detail string `json:"detail,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (d *Describer) describeEncoded(ctx context.Context, dataURI, question string) (string, error) {
	if strings.TrimSpace(question) == "" {
		question = defaultQuestion
	}

	payload := wireRequest{
		Model:     d.cfg.Model,
		MaxTokens: 2000,
		Messages: []wireMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: []any{
				textPart{Type: "text", Text: question},
				imagePart{Type: "image_url", ImageURL: imageURL{URL: dataURI, Detail: "high"}},
			}},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("vision: encode request: %w", err)
	}

	endpoint := strings.TrimRight(d.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("vision: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if d.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+d.cfg.APIKey)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("vision: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("vision: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vision: %s returned %d: %s", d.cfg.Model, resp.StatusCode, clip(string(raw), 300))
	}

	var wr wireResponse
	if err := json.Unmarshal(raw, &wr); err != nil {
		return "", fmt.Errorf("vision: decode response: %w", err)
	}
	if wr.Error != nil {
		return "", fmt.Errorf("vision: %s", wr.Error.Message)
	}
	if len(wr.Choices) == 0 {
		return "", errors.New("vision: the model returned no choices")
	}

	text := strings.TrimSpace(wr.Choices[0].Message.Content)
	if text == "" {
		// A text-only model accepts the payload and ignores the image
		// rather than refusing, so an empty answer is the usual symptom of
		// pointing this at the wrong model.
		return "", fmt.Errorf("vision: %s returned an empty description; check that it is a vision-capable model", d.cfg.Model)
	}
	return text, nil
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
