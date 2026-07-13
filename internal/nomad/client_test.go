package nomad

import (
	"testing"

	"github.com/hashicorp/nomad/api"
)

type emittedLine struct {
	line   string
	file   string
	offset int64
}

func TestFrameLinesTracksOffsetsAcrossFramesAndRotation(t *testing.T) {
	// Offsets are the file position after each frame's data.
	frames := []*api.StreamFrame{
		{File: "web.stderr.0", Offset: 100 + 9, Data: []byte("alpha\nbra")},
		{File: "web.stderr.0", Offset: 109 + 4, Data: []byte("vo\n\n")},
		{File: "web.stderr.1", Offset: 1 + 4, Data: []byte("tail")},
		{File: "web.stderr.1", Offset: 5 + 5, Data: []byte("s\nend")},
	}

	var got []emittedLine
	var lines frameLines
	emit := func(line, file string, offset int64) {
		got = append(got, emittedLine{line: line, file: file, offset: offset})
	}
	for _, frame := range frames {
		lines.add(frame, emit)
	}
	lines.flush(emit)

	want := []emittedLine{
		{line: "alpha", file: "web.stderr.0", offset: 100},
		{line: "bravo", file: "web.stderr.0", offset: 106},
		{line: "tails", file: "web.stderr.1", offset: 1},
		{line: "end", file: "web.stderr.1", offset: 7},
	}
	if len(got) != len(want) {
		t.Fatalf("emitted %d lines (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLineRef(t *testing.T) {
	if got := lineRef("web.stderr.0", 42); got != "web.stderr.0@42" {
		t.Fatalf("lineRef = %q", got)
	}
	if got := lineRef("", 42); got != "" {
		t.Fatalf("lineRef with empty file = %q, want empty", got)
	}
}

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
