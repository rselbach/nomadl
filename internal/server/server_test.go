package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestCrossOriginWritesRejected(t *testing.T) {
	tests := map[string]struct {
		fetchSite  string
		wantStatus int
		wantRows   int
	}{
		"cross-site clear rejected": {fetchSite: "cross-site", wantStatus: http.StatusForbidden, wantRows: 1},
		"same-origin clear allowed": {fetchSite: "same-origin", wantStatus: http.StatusNoContent, wantRows: 0},
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
			rows, err := srv.store.Count(t.Context())
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
