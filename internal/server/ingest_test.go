package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
	"github.com/rselbach/nomadl/internal/appconfig"
	"github.com/rselbach/nomadl/internal/store"
)

// fakeNomad serves the slice of the Nomad HTTP API that ingestion uses:
// the running-allocation list and followed task logs that grow while the
// test appends to them.
type fakeNomad struct {
	srv *httptest.Server

	mu      sync.Mutex
	allocs  []*api.AllocationListStub
	logs    map[string][]byte
	streams int
}

func newFakeNomad(t *testing.T) *fakeNomad {
	t.Helper()
	f := &fakeNomad{logs: make(map[string][]byte)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/allocations", f.handleAllocations)
	mux.HandleFunc("GET /v1/client/fs/logs/{alloc}", f.handleLogs)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNomad) setRunning(service, allocID, task string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allocs = append(f.allocs, &api.AllocationListStub{
		ID:           allocID,
		JobID:        service,
		NodeID:       "node-1",
		Namespace:    "default",
		ClientStatus: "running",
		TaskStates:   map[string]*api.TaskState{task: {State: "running"}},
	})
}

func (f *fakeNomad) stopAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allocs = nil
}

func (f *fakeNomad) appendLog(allocID, task, stream, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := allocID + "/" + task + "/" + stream
	f.logs[key] = append(f.logs[key], data...)
}

func (f *fakeNomad) openStreams() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streams
}

func (f *fakeNomad) handleAllocations(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(f.allocs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (f *fakeNomad) handleLogs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	key := r.PathValue("alloc") + "/" + query.Get("task") + "/" + query.Get("type")
	file := "alloc/logs/" + query.Get("task") + "." + query.Get("type") + ".0"
	back, err := strconv.Atoi(query.Get("offset"))
	if err != nil || query.Get("origin") != "end" || query.Get("follow") != "true" {
		http.Error(w, "unexpected log request "+r.URL.RawQuery, http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.streams++
	pos := max(len(f.logs[key])-back, 0)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.streams--
		f.mu.Unlock()
	}()

	flusher := w.(http.Flusher)
	enc := json.NewEncoder(w)
	for {
		f.mu.Lock()
		chunk := append([]byte(nil), f.logs[key][pos:]...)
		f.mu.Unlock()
		if len(chunk) > 0 {
			pos += len(chunk)
			if err := enc.Encode(api.StreamFrame{Offset: int64(pos), Data: chunk, File: file}); err != nil {
				return
			}
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func newIngestTestServer(t *testing.T, nomadAddr string) *Server {
	t.Helper()
	cfg := DefaultIngestConfig()
	cfg.ResetOnStart = false
	cfg.DiscoverInterval = 50 * time.Millisecond
	cfg.StreamStartDelay = 0
	s, err := New(filepath.Join(t.TempDir(), "ingest.db"), nomadAddr, cfg, appconfig.NewStore(t.TempDir()))
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

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func storedMessages(t *testing.T, st *store.Store) []string {
	t.Helper()
	entries, err := st.Search(store.SearchFilters{Limit: 100_000})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	messages := make([]string, 0, len(entries))
	for _, entry := range entries {
		messages = append(messages, entry.Message)
	}
	return messages
}

func TestIngestSurvivesDroppedStreamsWithoutGapsOrDuplicates(t *testing.T) {
	nomad := newFakeNomad(t)
	nomad.setRunning("greendale", "alloc-troy", "web")
	for i := 1; i <= 100; i++ {
		nomad.appendLog("alloc-troy", "web", "stderr", fmt.Sprintf("seq %d\n", i))
	}

	srv := newIngestTestServer(t, nomad.srv.URL)
	waitFor(t, "backfilled lines", func() bool {
		count, err := srv.store.Count()
		return err == nil && count == 100
	})

	// Write the rest in fragments that split lines across frames, and
	// drop every connection twice while writing.
	const total = 600
	for i := 101; i <= total; i++ {
		line := fmt.Sprintf("seq %d\n", i)
		nomad.appendLog("alloc-troy", "web", "stderr", line[:3])
		nomad.appendLog("alloc-troy", "web", "stderr", line[3:])
		if i == 250 || i == 450 {
			nomad.srv.CloseClientConnections()
		}
		time.Sleep(2 * time.Millisecond)
	}

	waitFor(t, "every line stored", func() bool {
		count, err := srv.store.Count()
		return err == nil && count >= total
	})

	got := storedMessages(t, srv.store)
	seen := make(map[string]int, len(got))
	for _, message := range got {
		seen[message]++
	}
	for i := 1; i <= total; i++ {
		want := fmt.Sprintf("seq %d", i)
		if seen[want] != 1 {
			t.Fatalf("%q stored %d times, want once", want, seen[want])
		}
	}
	if len(got) != total {
		t.Fatalf("stored %d rows, want %d", len(got), total)
	}
}

func TestIngestFollowsOnlyRunningAllowedTasks(t *testing.T) {
	nomad := newFakeNomad(t)
	nomad.setRunning("greendale", "alloc-abed", "web")
	nomad.setRunning("city-college", "alloc-chang", "web")

	srv := newIngestTestServer(t, nomad.srv.URL)
	waitFor(t, "both streams open", func() bool { return nomad.openStreams() == 2 })

	srv.ingest.setServices([]string{"greendale"})
	waitFor(t, "disallowed stream closed", func() bool {
		active := srv.ingest.status().active
		return nomad.openStreams() == 1 && len(active) == 1 && strings.HasPrefix(active[0], "greendale/")
	})

	// A followed log stream never ends on its own once its task stops, so
	// discovery has to close it.
	nomad.stopAll()
	waitFor(t, "stopped task's stream closed", func() bool {
		return nomad.openStreams() == 0 && len(srv.ingest.status().active) == 0
	})
	if got := srv.ingest.status().running; !slices.Equal(got, nil) {
		t.Fatalf("running services = %v, want none", got)
	}
}
