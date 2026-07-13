package nomad

import (
	"testing"
	"time"

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

func TestParseLogLineFormats(t *testing.T) {
	wantTime := time.Date(2026, 6, 27, 10, 11, 12, 0, time.UTC)

	tests := map[string]struct {
		line        string
		wantLevel   string
		wantMessage string
		wantTime    time.Time
	}{
		"logfmt": {
			line:        `ts=2026-06-27T10:11:12Z level=warn msg="Señor Chang locked the gym" component=security`,
			wantLevel:   "WARN",
			wantMessage: "Señor Chang locked the gym",
			wantTime:    wantTime,
		},
		"logfmt without msg": {
			line:      `time=2026-06-27T10:11:12Z level=error error="dean not found"`,
			wantLevel: "ERROR",
			wantTime:  wantTime,
		},
		"json epoch seconds": {
			line:        `{"ts":1782555072.5,"level":"info","msg":"paintball begins"}`,
			wantLevel:   "INFO",
			wantMessage: "paintball begins",
			wantTime:    time.Unix(1782555072, 500_000_000),
		},
		"json epoch milliseconds": {
			line:        `{"time":1782555072123,"level":"debug","message":"Abed Nadir calibrated"}`,
			wantLevel:   "DEBUG",
			wantMessage: "Abed Nadir calibrated",
			wantTime:    time.Unix(1782555072, 123_000_000),
		},
		"bracketed notice": {
			line:        `2026-06-27T10:11:12Z [NOTICE] Greendale Community College announcement`,
			wantLevel:   "NOTICE",
			wantMessage: "Greendale Community College announcement",
			wantTime:    wantTime,
		},
		"equals sign is not logfmt": {
			line:      `GET /path?a=b 200`,
			wantLevel: "UNKNOWN",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := parseLogLine(tc.line, "job", "alloc", "task", "stderr")
			if got.Level != tc.wantLevel {
				t.Fatalf("level = %q, want %q", got.Level, tc.wantLevel)
			}
			if tc.wantMessage != "" && got.Message != tc.wantMessage {
				t.Fatalf("message = %q, want %q", got.Message, tc.wantMessage)
			}
			if !tc.wantTime.IsZero() && !got.Timestamp.Equal(tc.wantTime) {
				t.Fatalf("timestamp = %v, want %v", got.Timestamp, tc.wantTime)
			}
			if got.Raw != tc.line {
				t.Fatalf("raw = %q, want %q", got.Raw, tc.line)
			}
		})
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
