package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestNewAppliesPragmas(t *testing.T) {
	s := newTestStore(t)

	tests := map[string]struct {
		db     *sql.DB
		pragma string
		want   string
	}{
		"writer journal mode":   {db: s.db, pragma: "journal_mode", want: "wal"},
		"writer busy timeout":   {db: s.db, pragma: "busy_timeout", want: "5000"},
		"writer synchronous":    {db: s.db, pragma: "synchronous", want: "1"},
		"reader busy timeout":   {db: s.ro, pragma: "busy_timeout", want: "5000"},
		"reader is query-only":  {db: s.ro, pragma: "query_only", want: "1"},
		"writer is not limited": {db: s.db, pragma: "query_only", want: "0"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got string
			if err := tc.db.QueryRow("PRAGMA " + tc.pragma).Scan(&got); err != nil {
				t.Fatalf("query %s: %v", tc.pragma, err)
			}
			if got != tc.want {
				t.Fatalf("%s = %q, want %q", tc.pragma, got, tc.want)
			}
		})
	}
}

func TestOpenReadDoesNotBlockWrites(t *testing.T) {
	s := newTestStore(t)
	insertTestLogs(t, s, []LogEntry{
		{Timestamp: time.Now(), Job: "greendale", AllocID: "a", Task: "t", Message: "Troy Barnes", LineRef: "f@1"},
		{Timestamp: time.Now(), Job: "greendale", AllocID: "a", Task: "t", Message: "Abed Nadir", LineRef: "f@2"},
	})

	// Hold a query open mid-iteration, as a slow search would.
	rows, err := s.ro.Query("SELECT id FROM logs")
	if err != nil {
		t.Fatalf("open query: %v", err)
	}
	if !rows.Next() {
		t.Fatalf("query returned no rows: %v", rows.Err())
	}

	done := make(chan error, 1)
	go func() {
		done <- s.InsertLogs([]LogEntry{{Timestamp: time.Now(), Job: "greendale", AllocID: "a", Task: "t", Message: "Annie Edison", LineRef: "f@3"}})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("insert during open read: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("insert blocked behind an open read")
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close rows: %v", err)
	}
}

func TestSearchOrdersMixedOffsetAndPrecisionTimestamps(t *testing.T) {
	s := newTestStore(t)
	berlin := time.FixedZone("CEST", 2*60*60)
	insertTestLogs(t, s, []LogEntry{
		{Timestamp: time.Date(2026, 6, 27, 12, 0, 0, 0, berlin), Job: "first", AllocID: "a", Task: "t", Level: "INFO", Message: "10:00Z as +02:00", Stream: "stderr"},
		{Timestamp: time.Date(2026, 6, 27, 10, 30, 0, 0, time.UTC), Job: "second", AllocID: "a", Task: "t", Level: "INFO", Message: "10:30Z", Stream: "stderr"},
		{Timestamp: time.Date(2026, 6, 27, 10, 30, 0, 500_000_000, time.UTC), Job: "third", AllocID: "a", Task: "t", Level: "INFO", Message: "10:30:00.5Z", Stream: "stderr"},
	})

	got, err := s.Search(t.Context(), SearchFilters{Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// Search returns newest first.
	want := []string{"third", "second", "first"}
	jobs := make([]string, 0, len(got))
	for _, entry := range got {
		jobs = append(jobs, entry.Job)
	}
	if !stringSlicesEqual(jobs, want) {
		t.Fatalf("jobs = %v, want %v", jobs, want)
	}
}

func TestInsertDeduplicatesByLineRef(t *testing.T) {
	s := newTestStore(t)
	entry := LogEntry{
		Timestamp: time.Date(2026, 6, 27, 10, 11, 12, 0, time.UTC),
		Job:       "study-group",
		AllocID:   "alloc-1",
		Task:      "dean",
		Level:     "INFO",
		Message:   "Human Being mascot unveiled",
		Raw:       "Human Being mascot unveiled",
		Stream:    "stderr",
		LineRef:   "dean.stderr.0@128",
	}

	// Same line refetched later gets a different fallback timestamp but
	// the same line ref; it must not create a second row.
	refetched := entry
	refetched.Timestamp = entry.Timestamp.Add(3 * time.Minute)
	insertTestLogs(t, s, []LogEntry{entry, refetched})

	count, err := s.Count(t.Context())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	// A genuine repeat of the same content at a different file position
	// is a distinct row.
	repeat := entry
	repeat.LineRef = "dean.stderr.0@256"
	insertTestLogs(t, s, []LogEntry{repeat})

	// Entries without a line ref (unknown position) are never deduped.
	noRef := entry
	noRef.LineRef = ""
	insertTestLogs(t, s, []LogEntry{noRef, noRef})

	count, err = s.Count(t.Context())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 4 {
		t.Fatalf("count = %d, want 4", count)
	}
}

func TestSearchReturnsRawPayload(t *testing.T) {
	s := newTestStore(t)

	raw := `{"time":"2026-06-27T10:11:12Z","level":"info","message":"Abed Nadir inspected the dreamatorium","trace_id":"greendale-42"}`
	entry := LogEntry{
		Timestamp:    time.Date(2026, 6, 27, 10, 11, 12, 0, time.UTC),
		Job:          "study-group",
		AllocID:      "alloc-1",
		Task:         "dreamatorium",
		Level:        "INFO",
		Message:      "Abed Nadir inspected the dreamatorium",
		Raw:          raw,
		Stream:       "stderr",
		TimeInferred: true,
	}

	insertTestLogs(t, s, []LogEntry{entry})

	got, err := s.Search(t.Context(), SearchFilters{Limit: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Raw != raw {
		t.Fatalf("raw = %q, want %q", got[0].Raw, raw)
	}
	if !got[0].TimeInferred {
		t.Fatal("time inferred flag lost on round trip")
	}
}

func TestSearchSupportsDatadogStyleQuerySyntax(t *testing.T) {
	s := newTestStore(t)
	insertTestLogs(t, s, []LogEntry{
		{
			Timestamp: time.Date(2026, 6, 27, 10, 11, 12, 0, time.UTC),
			Job:       "api",
			AllocID:   "alloc-api",
			Task:      "server",
			Level:     "ERROR",
			Message:   "payment timeout from Troy Barnes",
			Raw:       `{"trace_id":"greendale-42","http":{"status_code":503},"message":"payment timeout from Troy Barnes"}`,
			Stream:    "stderr",
		},
		{
			Timestamp: time.Date(2026, 6, 27, 10, 12, 12, 0, time.UTC),
			Job:       "web",
			AllocID:   "alloc-web",
			Task:      "frontend",
			Level:     "INFO",
			Message:   "Abed Nadir announced paintball tournament",
			Raw:       `{"trace_id":"greendale-99","http":{"status_code":200},"message":"Abed Nadir announced paintball tournament"}`,
			Stream:    "stdout",
		},
		{
			Timestamp: time.Date(2026, 6, 27, 10, 13, 12, 0, time.UTC),
			Job:       "worker",
			AllocID:   "alloc-worker",
			Task:      "queue",
			Level:     "WARN",
			Message:   "Señor Chang queued a suspicious job",
			Raw:       `{"trace_id":"greendale-125","duration_ms":125,"message":"Señor Chang queued a suspicious job"}`,
			Stream:    "stderr",
		},
		{
			Timestamp: time.Date(2026, 6, 27, 10, 14, 12, 0, time.UTC),
			Job:       "plain",
			AllocID:   "alloc-plain",
			Task:      "logger",
			Level:     "INFO",
			Message:   "plain text log with no json payload",
			Raw:       "plain text log with no json payload",
			Stream:    "stderr",
		},
	})

	tests := map[string]struct {
		query    string
		wantJobs []string
	}{
		"field group or":         {query: `service:(api OR web)`, wantJobs: []string{"api", "web"}},
		"free text implicit and": {query: `payment timeout`, wantJobs: []string{"api"}},
		"full text raw json":     {query: `*:greendale-42`, wantJobs: []string{"api"}},
		"json attribute":         {query: `@trace_id:greendale-42`, wantJobs: []string{"api"}},
		"json numeric compare":   {query: `@duration_ms:>100`, wantJobs: []string{"worker"}},
		"json numeric range":     {query: `@http.status_code:[500 TO 599]`, wantJobs: []string{"api"}},
		"negation":               {query: `service:(api OR web) -timeout`, wantJobs: []string{"web"}},
		"quoted phrase":          {query: `"paintball tournament"`, wantJobs: []string{"web"}},
		"status category":        {query: `status:error`, wantJobs: []string{"api"}},
		"unknown json field":     {query: `trace_id:greendale-99`, wantJobs: []string{"web"}},
		"wildcard field":         {query: `service:wor*`, wantJobs: []string{"worker"}},
		"message wildcard":       {query: `paint*`, wantJobs: []string{"web"}},
		"message inner wildcard": {query: `paint*tour`, wantJobs: []string{"web"}},
		"exact field wildcard":   {query: `service:*ork*`, wantJobs: []string{"worker"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := s.Search(t.Context(), SearchFilters{Query: tc.query, Limit: 10})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if gotJobs := sortedJobs(got); !stringSlicesEqual(gotJobs, tc.wantJobs) {
				t.Fatalf("jobs = %v, want %v", gotJobs, tc.wantJobs)
			}
		})
	}
}

func TestSearchTimeRangeAndPagination(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	entries := make([]LogEntry, 0, 5)
	for i := range 5 {
		entries = append(entries, LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Job:       "study-group",
			AllocID:   "a",
			Task:      "t",
			Level:     "INFO",
			Message:   fmt.Sprintf("event %d", i),
			Stream:    "stderr",
		})
	}
	insertTestLogs(t, s, entries)

	got, err := s.Search(t.Context(), SearchFilters{Since: base.Add(time.Minute), Until: base.Add(3 * time.Minute), Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("range returned %d rows, want 3", len(got))
	}
	if got[0].Message != "event 3" || got[2].Message != "event 1" {
		t.Fatalf("range rows = %q..%q, want event 3..event 1", got[0].Message, got[2].Message)
	}

	total, err := s.CountFiltered(t.Context(), SearchFilters{Since: base.Add(time.Minute)})
	if err != nil {
		t.Fatalf("count filtered: %v", err)
	}
	if total != 4 {
		t.Fatalf("count = %d, want 4", total)
	}
}

func TestSearchCursorPagesStayStableWhileRowsArrive(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	var entries []LogEntry
	for i := range 6 {
		// Pairs share a timestamp, so pages must break ties by id.
		entries = append(entries, LogEntry{
			Timestamp: base.Add(time.Duration(i/2) * time.Minute),
			Job:       "study-group", AllocID: "a", Task: "t",
			Message: fmt.Sprintf("event %d", i),
			LineRef: fmt.Sprintf("f@%d", i),
		})
	}
	insertTestLogs(t, s, entries)

	var got []string
	var after *Cursor
	for page := 0; ; page++ {
		rows, err := s.Search(t.Context(), SearchFilters{Limit: 2, After: after})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			got = append(got, row.Message)
		}
		cursor, err := ParseCursor(CursorAfter(rows[len(rows)-1]).String())
		if err != nil {
			t.Fatalf("round-trip cursor: %v", err)
		}
		after = &cursor

		// Newer rows arriving between pages must not shift later pages.
		insertTestLogs(t, s, []LogEntry{{
			Timestamp: base.Add(time.Hour + time.Duration(page)*time.Minute),
			Job:       "study-group", AllocID: "a", Task: "t",
			Message: fmt.Sprintf("late %d", page),
			LineRef: fmt.Sprintf("late@%d", page),
		}})
	}

	want := []string{"event 5", "event 4", "event 3", "event 2", "event 1", "event 0"}
	if !stringSlicesEqual(got, want) {
		t.Fatalf("pages = %v, want %v", got, want)
	}
}

func TestParseCursorRejectsGarbage(t *testing.T) {
	for _, value := range []string{"", "12", "abc-1", "1-abc"} {
		if _, err := ParseCursor(value); err == nil {
			t.Fatalf("ParseCursor(%q) succeeded, want error", value)
		}
	}
}

func TestPruneKeepsNewestRows(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	entries := make([]LogEntry, 0, 5)
	for i := range 5 {
		entries = append(entries, LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Job:       "study-group",
			AllocID:   "a",
			Task:      "t",
			Level:     "INFO",
			Message:   fmt.Sprintf("event %d", i),
			Stream:    "stderr",
		})
	}
	insertTestLogs(t, s, entries)

	deleted, err := s.Prune(3)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}

	got, err := s.Search(t.Context(), SearchFilters{Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 3 || got[0].Message != "event 4" || got[2].Message != "event 2" {
		t.Fatalf("remaining = %+v, want events 4..2", got)
	}

	// Fewer rows than the cap is a no-op, as is a zero cap.
	if deleted, err := s.Prune(10); err != nil || deleted != 0 {
		t.Fatalf("prune under cap = %d, %v; want 0, nil", deleted, err)
	}
	if deleted, err := s.Prune(0); err != nil || deleted != 0 {
		t.Fatalf("prune unlimited = %d, %v; want 0, nil", deleted, err)
	}
}

func TestHistogramCountsLevelBuckets(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	insertTestLogs(t, s, []LogEntry{
		{Timestamp: base, Job: "api", AllocID: "a", Task: "t", Level: "INFO", Message: "start", LineRef: "f@1"},
		{Timestamp: base.Add(32 * time.Second), Job: "api", AllocID: "a", Task: "t", Level: "ERR", Message: "boom", LineRef: "f@2"},
		{Timestamp: base.Add(34 * time.Second), Job: "api", AllocID: "a", Task: "t", Level: "WARNING", Message: "hmm", LineRef: "f@3"},
		{Timestamp: base.Add(60 * time.Second), Job: "api", AllocID: "a", Task: "t", Level: "SEVERE", Message: "end", LineRef: "f@4"},
	})

	h, err := s.Histogram(t.Context(), SearchFilters{}, 6)
	if err != nil {
		t.Fatalf("histogram: %v", err)
	}
	if h.Total != 4 || len(h.Bins) != 6 {
		t.Fatalf("total = %d bins = %d, want 4 and 6", h.Total, len(h.Bins))
	}
	if h.Bins[0].Levels["info"] != 1 || h.Bins[5].Levels["ok"] != 1 {
		t.Fatalf("edge bins = %+v / %+v, want info and ok", h.Bins[0], h.Bins[5])
	}
	middle := h.Bins[3]
	if middle.Count != 2 || middle.Levels["error"] != 1 || middle.Levels["warn"] != 1 {
		t.Fatalf("middle bin = %+v, want one error and one warn", middle)
	}

	since := base.Add(-time.Minute)
	until := base.Add(2 * time.Minute)
	window, err := s.Histogram(t.Context(), SearchFilters{Since: since, Until: until}, 3)
	if err != nil {
		t.Fatalf("windowed histogram: %v", err)
	}
	if !window.Start.Equal(since) || !window.End.Equal(until) || window.Total != 4 {
		t.Fatalf("window = %v..%v total %d, want the requested window with every row", window.Start, window.End, window.Total)
	}

	empty, err := s.Histogram(t.Context(), SearchFilters{Query: "service:nothing-matches"}, 6)
	if err != nil {
		t.Fatalf("empty histogram: %v", err)
	}
	if empty.Total != 0 || len(empty.Bins) != 0 {
		t.Fatalf("empty histogram = %+v, want zero", empty)
	}
}

func TestTraceIDQueryMatchesFlatAndNestedShapes(t *testing.T) {
	s := newTestStore(t)
	insertTestLogs(t, s, []LogEntry{
		{Timestamp: time.Date(2026, 6, 27, 10, 11, 12, 0, time.UTC), Job: "flat", AllocID: "a1", Task: "t", Level: "INFO", Message: "flat key",
			Raw: `{"message":"flat key","dd.trace_id":"greendale-1234"}`, Stream: "stderr"},
		{Timestamp: time.Date(2026, 6, 27, 10, 12, 12, 0, time.UTC), Job: "nested", AllocID: "a2", Task: "t", Level: "INFO", Message: "nested key",
			Raw: `{"message":"nested key","dd":{"trace_id":"greendale-1234","span_id":"7"}}`, Stream: "stderr"},
		{Timestamp: time.Date(2026, 6, 27, 10, 13, 12, 0, time.UTC), Job: "other", AllocID: "a3", Task: "t", Level: "INFO", Message: "different trace",
			Raw: `{"message":"different trace","dd":{"trace_id":"greendale-9999"}}`, Stream: "stderr"},
	})

	got, err := s.Search(t.Context(), SearchFilters{Query: `@dd.trace_id:greendale-1234`, Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := []string{"flat", "nested"}; !stringSlicesEqual(sortedJobs(got), want) {
		t.Fatalf("jobs = %v, want %v", sortedJobs(got), want)
	}
}

func TestLevelMatchesBuckets(t *testing.T) {
	s := newTestStore(t)
	insertTestLogs(t, s, []LogEntry{
		{Timestamp: time.Date(2026, 6, 27, 10, 11, 12, 0, time.UTC), Job: "short", AllocID: "a1", Task: "t", Level: "ERR", Message: "Troy Barnes"},
		{Timestamp: time.Date(2026, 6, 27, 10, 12, 12, 0, time.UTC), Job: "long", AllocID: "a2", Task: "t", Level: "ERROR", Message: "Abed Nadir"},
		{Timestamp: time.Date(2026, 6, 27, 10, 13, 12, 0, time.UTC), Job: "warning", AllocID: "a3", Task: "t", Level: "WARNING", Message: "Annie Edison"},
		{Timestamp: time.Date(2026, 6, 27, 10, 14, 12, 0, time.UTC), Job: "verbose", AllocID: "a4", Task: "t", Level: "VERBOSE", Message: "Jeff Winger"},
	})

	tests := map[string]struct {
		query    string
		wantJobs []string
	}{
		"level bucket":          {query: "level:error", wantJobs: []string{"long", "short"}},
		"level bucket any case": {query: "level:ERROR", wantJobs: []string{"long", "short"}},
		"warning spelling":      {query: "level:warning", wantJobs: []string{"warning"}},
		"status alias":          {query: "status:error", wantJobs: []string{"long", "short"}},
		"group of buckets":      {query: "level:(error OR warn)", wantJobs: []string{"long", "short", "warning"}},
		"raw level fallback":    {query: "level:verbose", wantJobs: []string{"verbose"}},
		"negated bucket":        {query: "-level:error", wantJobs: []string{"verbose", "warning"}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := s.Search(t.Context(), SearchFilters{Query: tc.query, Limit: 10})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if gotJobs := sortedJobs(got); !stringSlicesEqual(gotJobs, tc.wantJobs) {
				t.Fatalf("jobs = %v, want %v", gotJobs, tc.wantJobs)
			}
		})
	}
}

func TestStatusOkBucketCatchesUnrecognizedLevels(t *testing.T) {
	s := newTestStore(t)
	insertTestLogs(t, s, []LogEntry{
		{Timestamp: time.Date(2026, 6, 27, 10, 11, 12, 0, time.UTC), Job: "legacy", AllocID: "a1", Task: "t", Level: "SEVERE", Message: "old java logger", Stream: "stderr"},
		{Timestamp: time.Date(2026, 6, 27, 10, 12, 12, 0, time.UTC), Job: "plain", AllocID: "a2", Task: "t", Level: "UNKNOWN", Message: "no level detected", Stream: "stderr"},
		{Timestamp: time.Date(2026, 6, 27, 10, 13, 12, 0, time.UTC), Job: "api", AllocID: "a3", Task: "t", Level: "ERROR", Message: "boom", Stream: "stderr"},
	})

	got, err := s.Search(t.Context(), SearchFilters{Query: "status:ok", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := []string{"legacy", "plain"}; !stringSlicesEqual(sortedJobs(got), want) {
		t.Fatalf("status:ok jobs = %v, want %v", sortedJobs(got), want)
	}
}

func TestSuggestionContext(t *testing.T) {
	tests := map[string]struct {
		query       string
		cursor      int
		wantField   string
		wantPrefix  string
		wantStart   int
		wantEnd     int
		wantValue   bool
		wantNegated bool
	}{
		"field prefix": {
			query:      "serv",
			cursor:     len("serv"),
			wantPrefix: "serv",
			wantStart:  0,
			wantEnd:    len("serv"),
		},
		"negated field prefix": {
			query:       "-serv",
			cursor:      len("-serv"),
			wantPrefix:  "serv",
			wantStart:   0,
			wantEnd:     len("-serv"),
			wantNegated: true,
		},
		"field value": {
			query:      "service:cloud-",
			cursor:     len("service:cloud-"),
			wantField:  "service",
			wantPrefix: "cloud-",
			wantStart:  len("service:"),
			wantEnd:    len("service:cloud-"),
			wantValue:  true,
		},
		"grouped field value": {
			query:      "service:(api OR clo",
			cursor:     len("service:(api OR clo"),
			wantField:  "service",
			wantPrefix: "clo",
			wantStart:  len("service:(api OR "),
			wantEnd:    len("service:(api OR clo"),
			wantValue:  true,
		},
		"negated grouped field value": {
			query:       "service:(api OR -clo",
			cursor:      len("service:(api OR -clo"),
			wantField:   "service",
			wantPrefix:  "clo",
			wantStart:   len("service:(api OR -"),
			wantEnd:     len("service:(api OR -clo"),
			wantValue:   true,
			wantNegated: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := SuggestionContext(tc.query, tc.cursor)
			if got.Field != tc.wantField {
				t.Fatalf("field = %q, want %q", got.Field, tc.wantField)
			}
			if got.Prefix != tc.wantPrefix {
				t.Fatalf("prefix = %q, want %q", got.Prefix, tc.wantPrefix)
			}
			if got.ReplaceStart != tc.wantStart {
				t.Fatalf("replace start = %d, want %d", got.ReplaceStart, tc.wantStart)
			}
			if got.ReplaceEnd != tc.wantEnd {
				t.Fatalf("replace end = %d, want %d", got.ReplaceEnd, tc.wantEnd)
			}
			if got.ValueMode != tc.wantValue {
				t.Fatalf("value mode = %v, want %v", got.ValueMode, tc.wantValue)
			}
			if got.Negated != tc.wantNegated {
				t.Fatalf("negated = %v, want %v", got.Negated, tc.wantNegated)
			}
		})
	}
}

func TestStoreSuggestionSources(t *testing.T) {
	s := newTestStore(t)
	insertTestLogs(t, s, []LogEntry{
		{
			Timestamp: time.Date(2026, 6, 27, 10, 11, 12, 0, time.UTC),
			Job:       "cloud-idp",
			AllocID:   "alloc-idp",
			Task:      "server",
			Level:     "ERROR",
			Message:   "Troy Barnes hit the cloud-idp endpoint",
			Raw:       `{"trace_id":"greendale-42","http":{"status_code":503},"message":"Troy Barnes hit the cloud-idp endpoint"}`,
			Stream:    "stderr",
		},
		{
			Timestamp: time.Date(2026, 6, 27, 10, 12, 12, 0, time.UTC),
			Job:       "cloud-iam",
			AllocID:   "alloc-iam",
			Task:      "worker",
			Level:     "INFO",
			Message:   "Abed Nadir hit the cloud-iam endpoint",
			Raw:       `{"trace_id":"greendale-99","http":{"status_code":200},"message":"Abed Nadir hit the cloud-iam endpoint"}`,
			Stream:    "stdout",
		},
	})

	jobs, err := s.DistinctValues(t.Context(), "job", "cloud-i", 10)
	if err != nil {
		t.Fatalf("distinct jobs: %v", err)
	}
	if want := []string{"cloud-iam", "cloud-idp"}; !stringSlicesEqual(jobs, want) {
		t.Fatalf("jobs = %v, want %v", jobs, want)
	}

	attributes, err := s.JSONAttributeNames(t.Context(), "http.s", 10)
	if err != nil {
		t.Fatalf("json attribute names: %v", err)
	}
	if want := []string{"http.status_code"}; !stringSlicesEqual(attributes, want) {
		t.Fatalf("attributes = %v, want %v", attributes, want)
	}

	values, err := s.DistinctJSONValues(t.Context(), "trace_id", "greendale-9", 10)
	if err != nil {
		t.Fatalf("json values: %v", err)
	}
	if want := []string{"greendale-99"}; !stringSlicesEqual(values, want) {
		t.Fatalf("values = %v, want %v", values, want)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "nomadl.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return s
}

func insertTestLogs(t *testing.T, s *Store, entries []LogEntry) {
	t.Helper()
	if err := s.InsertLogs(entries); err != nil {
		t.Fatalf("insert logs: %v", err)
	}
}

func sortedJobs(entries []LogEntry) []string {
	jobs := make([]string, 0, len(entries))
	for _, entry := range entries {
		jobs = append(jobs, entry.Job)
	}
	sort.Strings(jobs)
	return jobs
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestQueryErrorsReportPosition(t *testing.T) {
	tests := map[string]struct {
		query   string
		wantPos int
	}{
		"unterminated quote":      {query: `service:api "Troy Barnes`, wantPos: 12},
		"unclosed group":          {query: `level:error (timeout OR refused`, wantPos: 12},
		"unclosed field group":    {query: `service:(api OR web`, wantPos: 8},
		"missing field value":     {query: `abed service:`, wantPos: 5},
		"dangling operator":       {query: `troy OR`, wantPos: 7},
		"stray close paren":       {query: `annie )`, wantPos: 6},
		"non-numeric comparison":  {query: `troy @http.status:>abc`, wantPos: 5},
		"leading space preserved": {query: `   "britta`, wantPos: 3},
	}

	s := newTestStore(t)
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := s.Search(t.Context(), SearchFilters{Query: tc.query})
			var queryErr *QueryError
			if !errors.As(err, &queryErr) {
				t.Fatalf("err = %v, want *QueryError", err)
			}
			if queryErr.Pos != tc.wantPos {
				t.Fatalf("pos = %d (%s), want %d", queryErr.Pos, queryErr.Msg, tc.wantPos)
			}
		})
	}
}

func TestSearchStopsWhenContextCancelled(t *testing.T) {
	s := newTestStore(t)
	insertTestLogs(t, s, []LogEntry{{Timestamp: time.Now(), Job: "greendale", AllocID: "a", Task: "t", Message: "Shirley Bennett"}})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Search(ctx, SearchFilters{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
