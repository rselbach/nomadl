package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSearchReturnsRawPayload(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "nomadl.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	}()

	raw := `{"time":"2026-06-27T10:11:12Z","level":"info","message":"Abed Nadir inspected the dreamatorium","trace_id":"greendale-42"}`
	entry := LogEntry{
		Timestamp: time.Date(2026, 6, 27, 10, 11, 12, 0, time.UTC),
		Job:       "study-group",
		AllocID:   "alloc-1",
		Task:      "dreamatorium",
		Level:     "INFO",
		Message:   "Abed Nadir inspected the dreamatorium",
		Raw:       raw,
		Stream:    "stderr",
	}

	if err := s.InsertLog(entry); err != nil {
		t.Fatalf("insert log: %v", err)
	}

	got, err := s.Search(SearchFilters{Limit: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Raw != raw {
		t.Fatalf("raw = %q, want %q", got[0].Raw, raw)
	}
}
