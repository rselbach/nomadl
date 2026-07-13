package store

import (
	"fmt"
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

	count, err := s.Count()
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
	if err := s.InsertLog(repeat); err != nil {
		t.Fatalf("insert repeat: %v", err)
	}

	// Entries without a line ref (unknown position) are never deduped.
	noRef := entry
	noRef.LineRef = ""
	insertTestLogs(t, s, []LogEntry{noRef, noRef})

	count, err = s.Count()
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

	got, err := s.Search(SearchFilters{Since: base.Add(time.Minute), Until: base.Add(3 * time.Minute), Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("range returned %d rows, want 3", len(got))
	}
	if got[0].Message != "event 3" || got[2].Message != "event 1" {
		t.Fatalf("range rows = %q..%q, want event 3..event 1", got[0].Message, got[2].Message)
	}

	total, err := s.CountFiltered(SearchFilters{Since: base.Add(time.Minute)})
	if err != nil {
		t.Fatalf("count filtered: %v", err)
	}
	if total != 4 {
		t.Fatalf("count = %d, want 4", total)
	}

	page, err := s.Search(SearchFilters{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("paged search: %v", err)
	}
	if len(page) != 2 || page[0].Message != "event 2" || page[1].Message != "event 1" {
		t.Fatalf("page = %+v, want events 2 and 1", page)
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

	got, err := s.Search(SearchFilters{Limit: 10})
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

func TestHistogramBucketsAndErrorCounts(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	insertTestLogs(t, s, []LogEntry{
		{Timestamp: base, Job: "api", AllocID: "a", Task: "t", Level: "INFO", Message: "start", Stream: "stderr"},
		{Timestamp: base.Add(30 * time.Second), Job: "api", AllocID: "a", Task: "t", Level: "ERROR", Message: "boom", Stream: "stderr"},
		{Timestamp: base.Add(60 * time.Second), Job: "api", AllocID: "a", Task: "t", Level: "INFO", Message: "end", Stream: "stderr"},
	})

	h, err := s.Histogram(SearchFilters{}, 6)
	if err != nil {
		t.Fatalf("histogram: %v", err)
	}
	if h.Total != 3 || h.Errors != 1 {
		t.Fatalf("total = %d errors = %d, want 3 and 1", h.Total, h.Errors)
	}
	if len(h.Bins) != 6 {
		t.Fatalf("bins = %d, want 6", len(h.Bins))
	}
	sum := 0
	errSum := 0
	for _, bin := range h.Bins {
		sum += bin.Count
		errSum += bin.Errors
	}
	if sum != 3 || errSum != 1 {
		t.Fatalf("bin sums = %d/%d, want 3/1", sum, errSum)
	}
	if h.Bins[0].Count != 1 || h.Bins[5].Count != 1 {
		t.Fatalf("edge bins = %d/%d, want 1/1", h.Bins[0].Count, h.Bins[5].Count)
	}
	if h.Bins[2].Errors+h.Bins[3].Errors != 1 {
		t.Fatalf("middle error not bucketed near center: %+v", h.Bins)
	}

	empty, err := s.Histogram(SearchFilters{Query: "service:nothing-matches"}, 6)
	if err != nil {
		t.Fatalf("empty histogram: %v", err)
	}
	if empty.Total != 0 || len(empty.Bins) != 0 {
		t.Fatalf("empty histogram = %+v, want zero", empty)
	}
}

func TestStatusOkBucketCatchesUnrecognizedLevels(t *testing.T) {
	s := newTestStore(t)
	insertTestLogs(t, s, []LogEntry{
		{Timestamp: time.Date(2026, 6, 27, 10, 11, 12, 0, time.UTC), Job: "legacy", AllocID: "a1", Task: "t", Level: "SEVERE", Message: "old java logger", Stream: "stderr"},
		{Timestamp: time.Date(2026, 6, 27, 10, 12, 12, 0, time.UTC), Job: "plain", AllocID: "a2", Task: "t", Level: "UNKNOWN", Message: "no level detected", Stream: "stderr"},
		{Timestamp: time.Date(2026, 6, 27, 10, 13, 12, 0, time.UTC), Job: "api", AllocID: "a3", Task: "t", Level: "ERROR", Message: "boom", Stream: "stderr"},
	})

	got, err := s.Search(SearchFilters{Query: "status:ok", Limit: 10})
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
