package store

import (
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestNewAppliesPragmas(t *testing.T) {
	s := newTestStore(t)

	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want %q", mode, "wal")
	}

	var timeout int
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", timeout)
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

	got, err := s.Search(SearchFilters{Limit: 10})
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

func TestSearchReturnsRawPayload(t *testing.T) {
	s := newTestStore(t)

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
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := s.Search(SearchFilters{Query: tc.query, Limit: 10})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if gotJobs := sortedJobs(got); !stringSlicesEqual(gotJobs, tc.wantJobs) {
				t.Fatalf("jobs = %v, want %v", gotJobs, tc.wantJobs)
			}
		})
	}
}

func TestMatchQueryUsesDatadogStyleSyntax(t *testing.T) {
	entry := LogEntry{
		Job:     "api",
		Task:    "server",
		Level:   "ERROR",
		Message: "payment timeout from Troy Barnes",
		Raw:     `{"trace_id":"greendale-42","http":{"status_code":503},"message":"payment timeout from Troy Barnes"}`,
		Stream:  "stderr",
	}

	match, err := MatchQuery(entry, `service:api status:error @http.status_code:[500 TO 599] -debug`)
	if err != nil {
		t.Fatalf("match query: %v", err)
	}
	if !match {
		t.Fatal("match = false, want true")
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

	jobs, err := s.DistinctValues("job", "cloud-i", 10)
	if err != nil {
		t.Fatalf("distinct jobs: %v", err)
	}
	if want := []string{"cloud-iam", "cloud-idp"}; !stringSlicesEqual(jobs, want) {
		t.Fatalf("jobs = %v, want %v", jobs, want)
	}

	attributes, err := s.JSONAttributeNames("http.s", 10)
	if err != nil {
		t.Fatalf("json attribute names: %v", err)
	}
	if want := []string{"http.status_code"}; !stringSlicesEqual(attributes, want) {
		t.Fatalf("attributes = %v, want %v", attributes, want)
	}

	values, err := s.DistinctJSONValues("trace_id", "greendale-9", 10)
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
