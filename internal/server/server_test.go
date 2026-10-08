package server

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rselbach/nomadl/internal/appconfig"
	"github.com/rselbach/nomadl/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := DefaultIngestConfig()
	cfg.Enabled = false
	cfg.ResetOnStart = false
	s, err := New(filepath.Join(t.TempDir(), "test.db"), "http://127.0.0.1:1", cfg, appconfig.NewStore(t.TempDir()))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	return s
}

func TestStreamSelectedTailsNewRowsFromStore(t *testing.T) {
	srv := newTestServer(t)

	// Pre-existing rows must not be replayed by the tail.
	old := store.LogEntry{
		Timestamp: time.Now().Add(-time.Minute),
		Job:       "study-group",
		AllocID:   "alloc-1",
		Task:      "dean",
		Level:     "INFO",
		Message:   "old entry before tail started",
		Stream:    "stderr",
	}
	if err := srv.store.InsertLogs([]store.LogEntry{old}); err != nil {
		t.Fatalf("insert old log: %v", err)
	}

	ts := httptest.NewServer(srv.mux)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/stream-selected?service=study-group")
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	t.Cleanup(func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close response body: %v", err)
		}
	})
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}

	fresh := old
	fresh.Timestamp = time.Now()
	fresh.Message = "Troy Barnes reported a fresh entry"
	if err := srv.store.InsertLogs([]store.LogEntry{fresh}); err != nil {
		t.Fatalf("insert fresh log: %v", err)
	}

	type event struct {
		name string
		data string
	}
	events := make(chan event, 10)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		current := event{}
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				current.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				current.data += strings.TrimPrefix(line, "data: ")
			case line == "":
				if current.name != "" {
					events <- current
				}
				current = event{}
			}
		}
	}()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case got := <-events:
			if got.name != "log" {
				continue
			}
			if strings.Contains(got.data, "old entry before tail started") {
				t.Fatalf("tail replayed pre-existing row: %q", got.data)
			}
			if !strings.Contains(got.data, "Troy Barnes reported a fresh entry") {
				t.Fatalf("unexpected log event: %q", got.data)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for tailed log event")
		}
	}
}

func TestTailCoverageNotice(t *testing.T) {
	srv := newTestServer(t)

	if notice := srv.tailCoverageNotice([]string{"api"}, "stderr"); !strings.Contains(notice, "Ingestion is disabled") {
		t.Fatalf("notice = %q, want ingestion-disabled warning", notice)
	}

	srv.ingest.mu.Lock()
	srv.ingest.cfg.Enabled = true
	srv.ingest.services = []string{"api"}
	srv.ingest.mu.Unlock()

	notice := srv.tailCoverageNotice([]string{"api", "web"}, "stdout")
	if !strings.Contains(notice, "web") {
		t.Fatalf("notice = %q, want missing-service warning for web", notice)
	}
	if !strings.Contains(notice, "stdout") {
		t.Fatalf("notice = %q, want stdout stream warning", notice)
	}
	if strings.Contains(notice, "api,") || strings.Contains(notice, ": api") {
		t.Fatalf("notice = %q, should not flag the ingested service", notice)
	}
}

func TestCrossOriginWritesRejected(t *testing.T) {
	tests := map[string]struct {
		fetchSite  string
		wantStatus int
		wantRows   int
	}{
		"cross-site clear rejected": {fetchSite: "cross-site", wantStatus: http.StatusForbidden, wantRows: 1},
		"same-origin clear allowed": {fetchSite: "same-origin", wantStatus: http.StatusOK, wantRows: 0},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := newTestServer(t)
			entry := store.LogEntry{Timestamp: time.Now(), Job: "study-group", AllocID: "alloc-1", Task: "dean", Message: "Annie Edison"}
			if err := srv.store.InsertLogs([]store.LogEntry{entry}); err != nil {
				t.Fatalf("insert log: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/api/clear", nil)
			req.Host = "127.0.0.1:7788"
			req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			rec := httptest.NewRecorder()
			srv.handler("127.0.0.1:7788").ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			rows, err := srv.store.Count()
			if err != nil {
				t.Fatalf("count rows: %v", err)
			}
			if rows != tc.wantRows {
				t.Fatalf("rows = %d, want %d", rows, tc.wantRows)
			}
		})
	}
}

func TestGuardLoopback(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	tests := map[string]struct {
		listenAddr string
		host       string
		wantStatus int
	}{
		"localhost allowed":           {listenAddr: "127.0.0.1:7788", host: "localhost:7788", wantStatus: http.StatusNoContent},
		"loopback ip allowed":         {listenAddr: "127.0.0.1:7788", host: "127.0.0.1:7788", wantStatus: http.StatusNoContent},
		"ipv6 loopback allowed":       {listenAddr: "127.0.0.1:7788", host: "[::1]:7788", wantStatus: http.StatusNoContent},
		"rebound name rejected":       {listenAddr: "127.0.0.1:7788", host: "greendale.example:7788", wantStatus: http.StatusForbidden},
		"non-loopback bind unguarded": {listenAddr: "0.0.0.0:7788", host: "greendale.example:7788", wantStatus: http.StatusNoContent},
		"loopback bind without port":  {listenAddr: "localhost:7788", host: "localhost", wantStatus: http.StatusNoContent},
		"rebound name without port":   {listenAddr: "localhost:7788", host: "greendale.example", wantStatus: http.StatusForbidden},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			handler := guardLoopback(tc.listenAddr, next)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}
