package agent

import (
	"encoding/json"
	"strings"
)

// wireContent is a message's content as a provider returns it: a string, or,
// as the OpenAI format also allows, a list of parts whose text parts make the
// answer. Some providers answer with the list even to a plain request.
type wireContent string

func (c *wireContent) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*c = wireContent(text)
		return nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &parts); err != nil {
		*c = ""
		return nil
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "" || p.Type == "text" || p.Type == "output_text" {
			b.WriteString(p.Text)
		}
	}
	*c = wireContent(b.String())
	return nil
}
