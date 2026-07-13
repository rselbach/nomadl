package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuardLoopback(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	tests := map[string]struct {
		listenAddr string
		host       string
		wantStatus int
	}{
		"localhost allowed":            {listenAddr: "127.0.0.1:7788", host: "localhost:7788", wantStatus: http.StatusNoContent},
		"loopback ip allowed":          {listenAddr: "127.0.0.1:7788", host: "127.0.0.1:7788", wantStatus: http.StatusNoContent},
		"ipv6 loopback allowed":        {listenAddr: "127.0.0.1:7788", host: "[::1]:7788", wantStatus: http.StatusNoContent},
		"rebound name rejected":        {listenAddr: "127.0.0.1:7788", host: "greendale.example:7788", wantStatus: http.StatusForbidden},
		"non-loopback bind unguarded":  {listenAddr: "0.0.0.0:7788", host: "greendale.example:7788", wantStatus: http.StatusNoContent},
		"loopback bind without port":   {listenAddr: "localhost:7788", host: "localhost", wantStatus: http.StatusNoContent},
		"rebound name without port":    {listenAddr: "localhost:7788", host: "greendale.example", wantStatus: http.StatusForbidden},
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
