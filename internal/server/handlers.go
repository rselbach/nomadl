package server

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
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
	"formatTime": func(t time.Time) string { return t.Format("2006-01-02 15:04:05") },
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

{{define "alloc-list"}}
{{if .}}
{{range $alloc := .}}
<div class="alloc-item">
  <div class="alloc-header">
    <span class="alloc-name">{{$alloc.Name}}</span>
    <span class="alloc-status status-{{$alloc.ClientStatus | lower}}">{{$alloc.ClientStatus}}</span>
  </div>
  {{range .Tasks}}
  <div class="task-row">
    <span class="task-name">{{.Name}}</span>
    <span class="task-state task-state-{{.State | lower}}">{{.State}}</span>
    <button class="btn-fetch" hx-post="/api/fetch?alloc={{$alloc.ID}}&task={{.Name}}" hx-target="#log-body" hx-swap="innerHTML">Fetch</button>
    <button class="btn-tail" onclick="startTail('{{$alloc.ID}}', '{{.Name}}')">Tail</button>
  </div>
  {{else}}
  <div class="task-row">
    <input class="task-input" placeholder="task name" aria-label="task name">
    <button class="btn-fetch" onclick="fetchManualTask(this, '{{$alloc.ID}}')">Fetch</button>
    <button class="btn-tail" onclick="tailManualTask(this, '{{$alloc.ID}}')">Tail</button>
  </div>
  {{end}}
</div>
{{end}}
{{else}}
<div class="loading">No allocations found</div>
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

func (s *Server) handleAllocations(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("job")
	if jobID == "" {
		http.Error(w, "job parameter required", http.StatusBadRequest)
		return
	}

	allocs, err := s.nomad.ListAllocations(jobID)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		writeHTMLf(w, `<div class="error-msg">Failed to load allocations: %s</div>`, html.EscapeString(err.Error()))
		return
	}
	render(w, "alloc-list", allocs)
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
	filters := filtersFromRequest(r)

	entries, err := s.store.Search(filters)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">Search failed: %s</td></tr>`, html.EscapeString(err.Error()))
		return
	}

	if len(entries) == 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		writeHTML(w, `<tr><td colspan="5" class="empty-state">No logs found yet. Ingestion may still be warming up, or filters are excluding everything.</td></tr>`)
		return
	}

	render(w, "log-list", entries)
}

func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	allocID := r.URL.Query().Get("alloc")
	task := r.URL.Query().Get("task")
	if allocID == "" || task == "" {
		http.Error(w, "alloc and task are required", http.StatusBadRequest)
		return
	}

	fetchBytes := int64(1 << 20)
	if b := r.URL.Query().Get("bytes"); b != "" {
		if v, err := strconv.ParseInt(b, 10, 64); err == nil && v > 0 {
			fetchBytes = v
		}
	}

	entries, err := s.nomad.FetchLogs(allocID, task, fetchBytes)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		writeHTMLf(w, `<tr><td colspan="5" class="error-msg">Failed to fetch logs: %s</td></tr>`, html.EscapeString(err.Error()))
		return
	}

	if len(entries) == 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		writeHTMLf(w, `<tr><td colspan="5" class="empty-state">No logs found for task %s</td></tr>`, html.EscapeString(task))
		return
	}

	if err := s.store.InsertLogs(entries); err != nil {
		fmt.Printf("warning: store logs: %v\n", err)
	}

	render(w, "log-list", entries)
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

	entries, err := s.store.Search(filtersFromRequest(r))
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

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	allocID := r.URL.Query().Get("alloc")
	task := r.URL.Query().Get("task")
	if allocID == "" || task == "" {
		http.Error(w, "alloc and task are required", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	entries, errCh := s.nomad.StreamLogs(allocID, task, ctx.Done())

	for {
		select {
		case entry, ok := <-entries:
			if !ok {
				return
			}
			if err := s.store.InsertLog(entry); err != nil {
				fmt.Printf("warning: store log: %v\n", err)
			}
			var b strings.Builder
			if err := tmpl.ExecuteTemplate(&b, "log-row", entry); err != nil {
				fmt.Printf("warning: render log row: %v\n", err)
				continue
			}
			if err := writeSSE(w, "log", b.String()); err != nil {
				fmt.Printf("warning: write SSE log: %v\n", err)
				return
			}
			flusher.Flush()

		case err := <-errCh:
			if err != nil {
				if err := writeSSE(w, "error", html.EscapeString(err.Error())); err != nil {
					fmt.Printf("warning: write SSE error: %v\n", err)
					return
				}
				flusher.Flush()
			}
			return

		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) handleStreamSelected(w http.ResponseWriter, r *http.Request) {
	services := selectedServices(r)
	if len(services) == 0 {
		http.Error(w, "select at least one service", http.StatusBadRequest)
		return
	}
	filters := filtersFromRequest(r)
	if err := store.ValidateQuery(filters.Query); err != nil {
		http.Error(w, fmt.Sprintf("invalid query: %v", err), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	entries, errs := s.streamServices(services, ctx.Done())
	for {
		select {
		case entry, ok := <-entries:
			if !ok {
				return
			}
			if !entryMatchesFilters(entry, filters) {
				continue
			}
			if err := s.store.InsertLog(entry); err != nil {
				fmt.Printf("warning: store log: %v\n", err)
			}
			var b strings.Builder
			if err := tmpl.ExecuteTemplate(&b, "log-row", entry); err != nil {
				fmt.Printf("warning: render log row: %v\n", err)
				continue
			}
			if err := writeSSE(w, "log", b.String()); err != nil {
				fmt.Printf("warning: write SSE log: %v\n", err)
				return
			}
			flusher.Flush()

		case err, ok := <-errs:
			if !ok {
				return
			}
			if err != nil {
				if err := writeSSE(w, "error", html.EscapeString(err.Error())); err != nil {
					fmt.Printf("warning: write SSE error: %v\n", err)
					return
				}
				flusher.Flush()
			}

		case <-ctx.Done():
			return
		}
	}
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

func filtersFromRequest(r *http.Request) store.SearchFilters {
	services := selectedServices(r)
	if r.URL.Query().Get("service_filter") == "1" && len(services) == 0 {
		services = []string{"__NO_SERVICE_SELECTED__"}
	}

	return store.SearchFilters{
		Query:  r.URL.Query().Get("q"),
		Level:  r.URL.Query().Get("level"),
		Levels: selectedStatusLevels(r),
		Stream: r.URL.Query().Get("stream"),
		Jobs:   services,
		Job:    r.URL.Query().Get("job"),
		Task:   r.URL.Query().Get("task"),
		Limit:  500,
	}
}

func selectedStatusLevels(r *http.Request) []string {
	if r.URL.Query().Get("status_filter") == "" {
		return nil
	}

	statusLevels := map[string][]string{
		"emergency": {"EMERGENCY", "ALERT", "CRITICAL", "CRIT", "FATAL", "PANIC"},
		"error":     {"ERROR", "ERR"},
		"warn":      {"WARN", "WARNING"},
		"notice":    {"NOTICE"},
		"info":      {"INFO"},
		"debug":     {"DEBUG", "TRACE"},
		"ok":        {"OK", "SUCCESS", "UNKNOWN"},
	}

	seen := make(map[string]struct{})
	levels := []string{}
	for _, status := range r.URL.Query()["status"] {
		for _, level := range statusLevels[status] {
			if _, ok := seen[level]; ok {
				continue
			}
			seen[level] = struct{}{}
			levels = append(levels, level)
		}
	}
	if len(levels) == 0 {
		return []string{"__NO_STATUS_SELECTED__"}
	}
	sort.Strings(levels)
	return levels
}

func entryMatchesFilters(entry store.LogEntry, filters store.SearchFilters) bool {
	if filters.Stream != "" && entry.Stream != filters.Stream {
		return false
	}
	if filters.Query != "" {
		matches, err := store.MatchQuery(entry, filters.Query)
		if err != nil {
			fmt.Printf("warning: match log query: %v\n", err)
			return false
		}
		if !matches {
			return false
		}
	}
	if len(filters.Levels) > 0 {
		for _, level := range filters.Levels {
			if entry.Level == level {
				return true
			}
		}
		return false
	}
	return true
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

func (s *Server) streamServices(services []string, cancel <-chan struct{}) (<-chan store.LogEntry, <-chan error) {
	entries := make(chan store.LogEntry, 100)
	errs := make(chan error, 10)

	go func() {
		defer close(entries)
		defer close(errs)

		targets, targetErrs := s.logTargets(services)
		for _, err := range targetErrs {
			errs <- err
		}

		var wg sync.WaitGroup
		sem := make(chan struct{}, 12)
		for _, target := range targets {
			target := target
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				stream, streamErrs := s.nomad.StreamLogs(target.allocID, target.task, cancel)
				for {
					select {
					case entry, ok := <-stream:
						if !ok {
							return
						}
						select {
						case entries <- entry:
						case <-cancel:
							return
						}
					case err, ok := <-streamErrs:
						if ok && err != nil {
							select {
							case errs <- fmt.Errorf("%s/%s: %w", target.service, target.task, err):
							case <-cancel:
							}
						}
						return
					case <-cancel:
						return
					}
				}
			}()
		}
		wg.Wait()
	}()

	return entries, errs
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
