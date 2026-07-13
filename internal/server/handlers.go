package server

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rselbach/nomadl/internal/nomad"
	"github.com/rselbach/nomadl/internal/store"
)

type settingsPayload struct {
	IngestServices    []string `json:"ingest_services"`
	AvailableServices []string `json:"available_services,omitempty"`
}

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"lower":      strings.ToLower,
	"formatTime": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	"levelClass": levelClass,
}).Parse(`
{{define "job-list"}}
{{if .}}
{{range .}}
<div class="service-item">
  <input type="checkbox" name="service" value="{{.ID}}" checked onchange="updateQueryFromServiceSidebar()">
  <button type="button" class="service-name" onclick="toggleOnlyService('{{.ID}}')">{{.Name}}</button>
</div>
{{end}}
{{else}}
<div class="loading">No services found</div>
{{end}}
{{end}}

{{define "log-row"}}
<tr class="log-row level-{{.Level | lower}}" data-log-entry="1" data-log-id="{{.ID}}" data-log-time="{{.Timestamp | formatTime}}" data-log-service="{{.Job}}" data-log-task="{{.Task}}" data-log-level="{{.Level}}" data-log-stream="{{.Stream}}" data-log-message="{{.Message}}" data-log-raw="{{.Raw}}">
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
// the grouping in store.levelsForStatus.
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
	jobs, err := s.nomad.ListJobs()
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		writeHTMLf(w, `<div class="error-msg">Failed to load jobs: %s</div>`, html.EscapeString(err.Error()))
		return
	}
	jobs = s.visibleJobs(jobs)
	render(w, "job-list", jobs)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.nomad.ListJobs()
	if err != nil {
		writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("load services: %v", err)})
		return
	}
	writeJSON(w, settingsPayload{
		IngestServices:    s.currentIngestServices(),
		AvailableServices: s.settingsServiceNames(jobs),
	})
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

	if err := s.updateIngestServices(payload.IngestServices); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	response := settingsPayload{IngestServices: s.currentIngestServices()}
	jobs, err := s.nomad.ListJobs()
	if err == nil {
		response.AvailableServices = s.settingsServiceNames(jobs)
	} else {
		fmt.Printf("warning: load services after settings save: %v\n", err)
	}
	writeJSON(w, response)
}

func (s *Server) visibleJobs(jobs []nomad.JobInfo) []nomad.JobInfo {
	if len(jobs) == 0 {
		return nil
	}

	byID := make(map[string]nomad.JobInfo, len(jobs))
	services := make([]string, 0, len(jobs))
	for _, job := range jobs {
		byID[job.ID] = job
		services = append(services, job.ID)
	}
	sort.Strings(services)
	services = filterServices(services, s.currentIngestServices())
	services = prioritizeServices(services, s.currentPriorityServices())

	visible := make([]nomad.JobInfo, 0, len(services))
	for _, service := range services {
		job, ok := byID[service]
		if !ok {
			continue
		}
		visible = append(visible, job)
	}
	return visible
}

func (s *Server) settingsServiceNames(jobs []nomad.JobInfo) []string {
	services := make([]string, 0, len(jobs))
	for _, job := range jobs {
		services = append(services, job.ID)
	}
	sort.Strings(services)
	services = prioritizeServices(services, s.currentPriorityServices())
	return mergeServiceLists(s.currentIngestServices(), services)
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

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	jobs, err := s.nomad.ListJobs()
	if err != nil {
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">Diagnostics failed: Nomad API %s returned: %s</td></tr>`, html.EscapeString(s.nomad.Address()), html.EscapeString(err.Error()))
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, `Nomad API: %s<br>Jobs visible: %d`, html.EscapeString(s.nomad.Address()), len(jobs))
	if len(jobs) > 0 {
		allocs, err := s.nomad.ListAllocations(jobs[0].ID)
		if err != nil {
			fmt.Fprintf(&b, `<br>First job: %s<br>Allocation check failed: %s`, html.EscapeString(jobs[0].ID), html.EscapeString(err.Error()))
		} else {
			fmt.Fprintf(&b, `<br>First job: %s<br>Allocations visible: %d`, html.EscapeString(jobs[0].ID), len(allocs))
			if len(allocs) > 0 {
				fmt.Fprintf(&b, `<br>First allocation: %s<br>Tasks visible: %d`, html.EscapeString(allocs[0].ID), len(allocs[0].Tasks))
			}
		}
	}
	fmt.Fprintf(&b, `<br><br>If Fetch still returns no rows, the next message should now show the real Nomad log API error instead of hiding it.`)

	writeHTMLf(w, `<tr><td colspan="5" class="empty-state">%s</td></tr>`, b.String())
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	filters, err := filtersFromRequest(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">%s</td></tr>`, html.EscapeString(err.Error()))
		return
	}

	entries, err := s.store.Search(filters)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">Search failed: %s</td></tr>`, html.EscapeString(err.Error()))
		return
	}

	total, err := s.store.CountFiltered(filters)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">Count failed: %s</td></tr>`, html.EscapeString(err.Error()))
		return
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))

	if len(entries) == 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Offset pages past the end render nothing so the client can
		// append the response verbatim.
		if filters.Offset == 0 {
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

	h, err := s.store.Histogram(filters, 60)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	response := histogramResponse{
		Total:  h.Total,
		Errors: h.Errors,
		Bins:   make([]histogramBin, 0, len(h.Bins)),
	}
	if h.Total > 0 {
		response.StartMS = h.Start.UnixMilli()
		response.EndMS = h.End.UnixMilli()
		response.IntervalMS = h.Interval.Milliseconds()
	}
	for _, bin := range h.Bins {
		response.Bins = append(response.Bins, histogramBin{Count: bin.Count, Errors: bin.Errors})
	}
	writeJSON(w, response)
}

func (s *Server) handleFetchSelected(w http.ResponseWriter, r *http.Request) {
	services := selectedServices(r)
	if len(services) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		writeHTML(w, `<tr><td colspan="5" class="empty-state">Select at least one service.</td></tr>`)
		return
	}

	fetchBytes := int64(256 << 10)
	if b := r.URL.Query().Get("bytes"); b != "" {
		if v, err := strconv.ParseInt(b, 10, 64); err == nil && v > 0 {
			fetchBytes = v
		}
	}

	entries, errs := s.fetchServices(services, fetchBytes)
	if len(entries) == 0 {
		w.WriteHeader(http.StatusBadGateway)
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">No logs fetched. %s</td></tr>`, html.EscapeString(joinErrors(errs)))
		return
	}

	if err := s.store.InsertLogs(entries); err != nil {
		fmt.Printf("warning: store logs: %v\n", err)
	}

	filters, err := filtersFromRequest(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">%s</td></tr>`, html.EscapeString(err.Error()))
		return
	}
	entries, err = s.store.Search(filters)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">Search failed after fetch: %s</td></tr>`, html.EscapeString(err.Error()))
		return
	}
	if len(entries) == 0 {
		writeHTML(w, `<tr><td colspan="5" class="empty-state">Logs fetched, but no rows match the current filters.</td></tr>`)
		return
	}

	render(w, "log-list", entries)
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

	lastID, err := s.store.MaxID()
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
			entries, err := s.store.SearchAfter(lastID, filters)
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
	s.ingestMu.Lock()
	cfg := s.ingestCfg
	s.ingestMu.Unlock()

	if !cfg.Enabled {
		return "Ingestion is disabled (-ingest=false), so live tail will not receive logs."
	}

	var notes []string
	if allowlist := s.currentIngestServices(); len(allowlist) > 0 {
		allowed := make(map[string]struct{}, len(allowlist))
		for _, service := range allowlist {
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
	if stream != "" && !slices.Contains(cfg.Streams, stream) {
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
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return store.SearchFilters{}, fmt.Errorf("invalid offset %q", v)
		}
		filters.Offset = n
	}
	return filters, nil
}

func (s *Server) fetchServices(services []string, fetchBytes int64) ([]store.LogEntry, []error) {
	targets, errs := s.logTargets(services)
	if len(targets) == 0 {
		return nil, errs
	}

	var entries []store.LogEntry
	var fetchErrs []error
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for _, target := range targets {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			logs, err := s.nomad.FetchLogs(target.allocID, target.task, fetchBytes)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fetchErrs = append(fetchErrs, fmt.Errorf("%s/%s: %w", target.service, target.task, err))
				return
			}
			entries = append(entries, logs...)
		}()
	}
	wg.Wait()

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	return entries, append(errs, fetchErrs...)
}

type logTarget struct {
	service string
	allocID string
	task    string
}

func (s *Server) logTargets(services []string) ([]logTarget, []error) {
	var targets []logTarget
	var errs []error
	for _, service := range services {
		allocs, err := s.nomad.ListAllocations(service)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s allocations: %w", service, err))
			continue
		}
		for _, alloc := range allocs {
			if alloc.ClientStatus != "running" {
				continue
			}
			for _, task := range alloc.Tasks {
				if task.Name == "" {
					continue
				}
				targets = append(targets, logTarget{
					service: service,
					allocID: alloc.ID,
					task:    task.Name,
				})
			}
		}
	}
	return targets, errs
}

func joinErrors(errs []error) string {
	if len(errs) == 0 {
		return "No allocation tasks were available for selected services."
	}
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "; ")
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Clear(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">Failed to clear: %s</td></tr>`, html.EscapeString(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	writeHTML(w, `<tr><td colspan="5" class="empty-state">Logs cleared. Select services and fetch logs or start live tail.</td></tr>`)
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
