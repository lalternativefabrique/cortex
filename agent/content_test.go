package agent

import (
	"encoding/json"
	"testing"
)

func TestContentReadsAStringOrTheTextOfItsParts(t *testing.T) {
	for raw, want := range map[string]string{
		`"plain"`: "plain",
		`[{"type":"text","text":"one "},{"type":"thinking","text":"x"},{"type":"text","text":"two"}]`: "one two",
		`null`: "",
	} {
		var c wireContent
		if err := json.Unmarshal([]byte(raw), &c); err != nil || string(c) != want {
			t.Errorf("%s: got %q, %v; want %q", raw, c, err, want)
		}
	}
}
