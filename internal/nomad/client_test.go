package nomad

import "testing"

func TestParseLogLinePreservesRawJSON(t *testing.T) {
	raw := `{"time":"2026-06-27T10:11:12Z","level":"error","message":"Troy Barnes triggered the fire alarm","request_id":"greendale-7"}`

	got := parseLogLine(raw, "study-group", "alloc-1", "dean", "stderr")

	if got.Raw != raw {
		t.Fatalf("raw = %q, want %q", got.Raw, raw)
	}
	if got.Message != "Troy Barnes triggered the fire alarm" {
		t.Fatalf("message = %q", got.Message)
	}
	if got.Level != "ERROR" {
		t.Fatalf("level = %q", got.Level)
	}
	if got.Stream != "stderr" {
		t.Fatalf("stream = %q", got.Stream)
	}
}
