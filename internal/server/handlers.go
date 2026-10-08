package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rselbach/nomadl/internal/appconfig"
	"github.com/rselbach/nomadl/internal/store"
)

type settingsPayload struct {
	IngestServices    []string `json:"ingest_services"`
	TraceFields       []string `json:"trace_fields"`
	AvailableServices []string `json:"available_services,omitempty"`
	NomadError        string   `json:"nomad_error,omitempty"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s.ingest.waitFirstDiscovery(r.Context())
	payload, err := s.settingsPayload(r.Context())
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, payload)
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer func() {
		if err := r.Body.Close(); err != nil {
			slog.Warn("close settings request body", "err", err)
		}
	}()

	var payload settingsPayload
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("decode settings: %v", err)})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "settings request must contain one JSON object"})
		return
	}

	if err := s.saveSettings(appconfig.Settings{IngestServices: payload.IngestServices, TraceFields: payload.TraceFields}); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	response, err := s.settingsPayload(r.Context())
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, response)
}

// settingsPayload reports the current allowlist and trace fields. It
// lists the allowlist, then every running service (priority services
// first), then services known only from stored logs, so services can be
// chosen while Nomad is unreachable.
func (s *Server) settingsPayload(ctx context.Context) (settingsPayload, error) {
	saved, err := s.settingsStore.Load()
	if err != nil {
		return settingsPayload{}, err
	}
	traceFields := saved.TraceFields
	if len(traceFields) == 0 {
		traceFields = appconfig.DefaultTraceFields
	}
	stored, err := s.store.DistinctValues(ctx, "job", "", 1000)
	if err != nil {
		return settingsPayload{}, err
	}

	status := s.ingest.status()
	running := prioritizeServices(status.running, s.ingest.cfg.PriorityServices)
	payload := settingsPayload{
		IngestServices:    status.services,
		TraceFields:       traceFields,
		AvailableServices: mergeServiceLists(status.services, running, stored),
	}
	if status.nomadErr != nil {
		payload.NomadError = status.nomadErr.Error()
	}
	return payload, nil
}

func mergeServiceLists(lists ...[]string) []string {
	var merged []string
	seen := make(map[string]struct{})
	for _, list := range lists {
		for _, service := range list {
			service = strings.TrimSpace(service)
			if service == "" {
				continue
			}
			if _, ok := seen[service]; ok {
				continue
			}
			seen[service] = struct{}{}
			merged = append(merged, service)
		}
	}
	return merged
}

type statusResponse struct {
	NomadAddr      string   `json:"nomad_addr"`
	NomadError     string   `json:"nomad_error,omitempty"`
	DBRows         int      `json:"db_rows"`
	IngestEnabled  bool     `json:"ingest_enabled"`
	IngestServices []string `json:"ingest_services,omitempty"`
	Streams        []string `json:"streams,omitempty"`
	MaxStreams     int      `json:"max_streams"`
	ActiveStreams  []string `json:"active_streams"`
	WaitingStreams []string `json:"waiting_streams"`
	LastDiscovery  string   `json:"last_discovery,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := s.ingest.status()
	response := statusResponse{
		NomadAddr:      s.nomad.Address(),
		IngestEnabled:  status.enabled,
		IngestServices: status.services,
		Streams:        status.streams,
		MaxStreams:     status.maxStreams,
		ActiveStreams:  status.active,
		WaitingStreams: status.waiting,
	}
	if status.nomadErr != nil {
		response.NomadError = status.nomadErr.Error()
	}
	if !status.lastDiscovery.IsZero() {
		response.LastDiscovery = status.lastDiscovery.Local().Format("2006-01-02 15:04:05")
	}

	rows, err := s.store.Count(r.Context())
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	response.DBRows = rows
	writeJSON(w, response)
}

func filtersFromRequest(r *http.Request) (store.SearchFilters, error) {
	filters := store.SearchFilters{Query: r.URL.Query().Get("q")}

	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return store.SearchFilters{}, fmt.Errorf("invalid since %q: must be RFC3339", v)
		}
		filters.Since = t
	}
	if v := r.URL.Query().Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return store.SearchFilters{}, fmt.Errorf("invalid until %q: must be RFC3339", v)
		}
		filters.Until = t
	}
	if v := r.URL.Query().Get("cursor"); v != "" {
		cursor, err := store.ParseCursor(v)
		if err != nil {
			return store.SearchFilters{}, err
		}
		filters.After = &cursor
	}
	return filters, nil
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Clear(r.Context()); err != nil {
		writeJSONError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// errorStatus maps an error from the store to an HTTP status: an invalid
// query is the client's mistake, anything else is the server's.
func errorStatus(err error) int {
	var queryErr *store.QueryError
	if errors.As(err, &queryErr) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// writeJSONError reports err as {"error": ..., "pos": ...}; pos is the
// byte offset of an invalid query's problem and is absent otherwise.
func writeJSONError(w http.ResponseWriter, err error) {
	body := map[string]any{"error": err.Error()}
	var queryErr *store.QueryError
	if errors.As(err, &queryErr) {
		body["pos"] = queryErr.Pos
	}
	writeJSONStatus(w, errorStatus(err), body)
}

func writeJSON(w http.ResponseWriter, value any) {
	writeJSONStatus(w, http.StatusOK, value)
}

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Warn("write JSON response", "err", err)
	}
}
