package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
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

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"lower":      strings.ToLower,
	"formatTime": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	"epochMS":    func(t time.Time) int64 { return t.UnixMilli() },
	"levelClass": levelClass,
}).Parse(`
{{define "job-list"}}
{{if .}}
{{range .}}
<div class="service-item">
  <input type="checkbox" name="service" value="{{.}}" checked onchange="updateQueryFromServiceSidebar()">
  <button type="button" class="service-name" onclick="toggleOnlyService('{{.}}')">{{.}}</button>
</div>
{{end}}
{{else}}
<div class="loading">No services found</div>
{{end}}
{{end}}

{{define "log-row"}}
<tr class="log-row level-{{.Level | lower}}" data-log-entry="1" data-log-id="{{.ID}}" data-log-time="{{.Timestamp | formatTime}}" data-log-epoch="{{.Timestamp | epochMS}}"{{if .TimeInferred}} data-log-inferred="1"{{end}} data-log-service="{{.Job}}" data-log-task="{{.Task}}" data-log-level="{{.Level}}" data-log-stream="{{.Stream}}" data-log-message="{{.Message}}" data-log-raw="{{.Raw}}">
  <td class="log-time">{{.Timestamp | formatTime}}</td>
  <td class="log-level">{{if .Level}}<span class="lvl-badge lvl-{{.Level | levelClass}}">{{.Level}}</span>{{end}}</td>
  <td class="log-service">{{.Job}}</td>
  <td class="log-task">{{.Task}}</td>
  <td class="log-message">{{.Message}}</td>
</tr>
{{end}}

{{define "log-list"}}
{{range .}}{{template "log-row" .}}{{end}}
{{end}}
`))

// levelClass buckets raw log levels into the CSS badge classes; it mirrors
// the grouping in store.levelsForBucket.
func levelClass(level string) string {
	switch strings.ToLower(level) {
	case "emergency", "alert", "critical", "crit", "fatal", "panic":
		return "emergency"
	case "error", "err":
		return "error"
	case "warn", "warning":
		return "warn"
	case "notice":
		return "notice"
	case "info":
		return "info"
	case "debug", "trace":
		return "debug"
	case "ok", "success", "unknown":
		return "ok"
	default:
		return "none"
	}
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	s.ingest.waitFirstDiscovery(r.Context())
	status := s.ingest.status()
	services := s.ingest.visibleServices()
	if status.nomadErr != nil && len(services) == 0 {
		w.WriteHeader(http.StatusBadGateway)
		writeHTMLf(w, `<div class="error-msg">Failed to load services: %s</div>`, html.EscapeString(status.nomadErr.Error()))
		return
	}
	render(w, "job-list", services)
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
			fmt.Printf("warning: close settings request body: %v\n", err)
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
	JobsVisible    int      `json:"jobs_visible"`
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
		JobsVisible:    len(s.ingest.visibleServices()),
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

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	filters, err := filtersFromRequest(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">%s</td></tr>`, html.EscapeString(err.Error()))
		return
	}

	entries, err := s.store.Search(r.Context(), filters)
	if err != nil {
		w.WriteHeader(errorStatus(err))
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">Search failed: %s</td></tr>`, html.EscapeString(err.Error()))
		return
	}

	total, err := s.store.CountFiltered(r.Context(), filters)
	if err != nil {
		w.WriteHeader(errorStatus(err))
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">Count failed: %s</td></tr>`, html.EscapeString(err.Error()))
		return
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	if len(entries) == filters.Limit {
		w.Header().Set("X-Next-Cursor", store.CursorAfter(entries[len(entries)-1]).String())
	}

	if len(entries) == 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Pages past the end render nothing so the client can append
		// the response verbatim.
		if filters.After == nil {
			writeHTML(w, `<tr><td colspan="5" class="empty-state">No logs found yet. Ingestion may still be warming up, or filters are excluding everything.</td></tr>`)
		}
		return
	}

	render(w, "log-list", entries)
}

type histogramResponse struct {
	StartMS    int64          `json:"start_ms"`
	EndMS      int64          `json:"end_ms"`
	IntervalMS int64          `json:"interval_ms"`
	Total      int            `json:"total"`
	Errors     int            `json:"errors"`
	Bins       []histogramBin `json:"bins"`
}

type histogramBin struct {
	Count  int `json:"count"`
	Errors int `json:"errors"`
}

func (s *Server) handleHistogram(w http.ResponseWriter, r *http.Request) {
	filters, err := filtersFromRequest(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	h, err := s.store.Histogram(r.Context(), filters, 60)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	response := histogramResponse{
		Total: h.Total,
		Bins:  make([]histogramBin, 0, len(h.Bins)),
	}
	if h.Total > 0 {
		response.StartMS = h.Start.UnixMilli()
		response.EndMS = h.End.UnixMilli()
		response.IntervalMS = h.Interval.Milliseconds()
	}
	for _, bin := range h.Bins {
		errors := bin.Levels["error"] + bin.Levels["emergency"]
		response.Errors += errors
		response.Bins = append(response.Bins, histogramBin{Count: bin.Count, Errors: errors})
	}
	writeJSON(w, response)
}

// handleStreamSelected tails new log rows from the store over SSE. The
// background ingester is the single pipeline from Nomad into SQLite;
// tailing from the store means redeployed allocations keep flowing
// (the ingester re-discovers them), the query semantics are exactly
// those of /api/search, and no second set of Nomad connections is
// opened per tail.
func (s *Server) handleStreamSelected(w http.ResponseWriter, r *http.Request) {
	services := selectedServices(r)
	if len(services) == 0 {
		http.Error(w, "select at least one service", http.StatusBadRequest)
		return
	}
	filters, err := filtersFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := store.ValidateQuery(filters.Query); err != nil {
		http.Error(w, fmt.Sprintf("invalid query: %v", err), http.StatusBadRequest)
		return
	}

	lastID, err := s.store.MaxID(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("resolve tail position: %v", err), http.StatusInternalServerError)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if notice := s.tailCoverageNotice(services, filters.Stream); notice != "" {
		if err := writeSSE(w, "notice", html.EscapeString(notice)); err != nil {
			fmt.Printf("warning: write SSE notice: %v\n", err)
			return
		}
	}
	flusher.Flush()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ticker.C:
			entries, err := s.store.SearchAfter(ctx, lastID, filters)
			if err != nil {
				if err := writeSSE(w, "stream-error", html.EscapeString(err.Error())); err != nil {
					fmt.Printf("warning: write SSE error: %v\n", err)
				}
				flusher.Flush()
				return
			}
			if len(entries) == 0 {
				continue
			}
			for _, entry := range entries {
				lastID = entry.ID
				var b strings.Builder
				if err := tmpl.ExecuteTemplate(&b, "log-row", entry); err != nil {
					fmt.Printf("warning: render log row: %v\n", err)
					continue
				}
				if err := writeSSE(w, "log", b.String()); err != nil {
					fmt.Printf("warning: write SSE log: %v\n", err)
					return
				}
			}
			flusher.Flush()

		case <-ctx.Done():
			return
		}
	}
}

// tailCoverageNotice explains gaps between what the user asked to tail
// and what the ingester actually writes to the store.
func (s *Server) tailCoverageNotice(services []string, stream string) string {
	status := s.ingest.status()
	if !status.enabled {
		return "Ingestion is disabled (-ingest=false), so live tail will not receive logs."
	}

	var notes []string
	if len(status.services) > 0 {
		allowed := make(map[string]struct{}, len(status.services))
		for _, service := range status.services {
			allowed[service] = struct{}{}
		}
		var missing []string
		for _, service := range services {
			if _, ok := allowed[service]; !ok {
				missing = append(missing, service)
			}
		}
		if len(missing) > 0 {
			notes = append(notes, fmt.Sprintf("Not being ingested (enable in Settings): %s.", strings.Join(missing, ", ")))
		}
	}
	if stream != "" && !slices.Contains(status.streams, stream) {
		notes = append(notes, fmt.Sprintf("The %s stream is not ingested (see -ingest-stdout).", stream))
	}
	return strings.Join(notes, " ")
}

func selectedServices(r *http.Request) []string {
	values := r.URL.Query()["service"]
	services := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		services = append(services, value)
	}
	sort.Strings(services)
	return services
}

func filtersFromRequest(r *http.Request) (store.SearchFilters, error) {
	filters := store.SearchFilters{
		Query:  r.URL.Query().Get("q"),
		Stream: r.URL.Query().Get("stream"),
		Jobs:   selectedServices(r),
		Limit:  500,
	}

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
		w.WriteHeader(http.StatusInternalServerError)
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">Failed to clear: %s</td></tr>`, html.EscapeString(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	writeHTML(w, `<tr><td colspan="5" class="empty-state">Logs cleared.</td></tr>`)
}

func render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		fmt.Printf("warning: render template %s: %v\n", name, err)
	}
}

func writeHTML(w http.ResponseWriter, content string) {
	if _, err := fmt.Fprint(w, content); err != nil {
		fmt.Printf("warning: write response: %v\n", err)
	}
}

func writeHTMLf(w http.ResponseWriter, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		fmt.Printf("warning: write response: %v\n", err)
	}
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
		fmt.Printf("warning: write JSON response: %v\n", err)
	}
}

func writeSSE(w http.ResponseWriter, event, data string) error {
	lines := strings.Split(data, "\n")
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	for _, line := range lines {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w, "\n")
	return err
}
