package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/rselbach/nomadl/internal/store"
)

const (
	defaultPageSize = 200
	maxPageSize     = 1000
	histogramBins   = 60
	maxContextLines = 500
)

// rowJSON is one log row as the UI consumes it.
type rowJSON struct {
	ID           int64  `json:"id"`
	TimeMS       int64  `json:"ts"`
	TimeInferred bool   `json:"ts_inferred,omitempty"`
	Service      string `json:"service"`
	AllocID      string `json:"alloc_id"`
	Task         string `json:"task"`
	Stream       string `json:"stream"`
	Level        string `json:"level"`
	LevelBucket  string `json:"level_bucket"`
	Message      string `json:"message"`
	// Raw is omitted when it equals Message.
	Raw string `json:"raw,omitempty"`
}

func toRows(entries []store.LogEntry) []rowJSON {
	rows := make([]rowJSON, 0, len(entries))
	for _, e := range entries {
		row := rowJSON{
			ID:           e.ID,
			TimeMS:       e.Timestamp.UnixMilli(),
			TimeInferred: e.TimeInferred,
			Service:      e.Job,
			AllocID:      e.AllocID,
			Task:         e.Task,
			Stream:       e.Stream,
			Level:        e.Level,
			LevelBucket:  store.LevelBucket(e.Level),
			Message:      e.Message,
		}
		if e.Raw != e.Message {
			row.Raw = e.Raw
		}
		rows = append(rows, row)
	}
	return rows
}

type histogramJSON struct {
	StartMS int64              `json:"start_ms"`
	EndMS   int64              `json:"end_ms"`
	Bins    []histogramBinJSON `json:"bins"`
}

type histogramBinJSON struct {
	Count  int            `json:"count"`
	Levels map[string]int `json:"levels,omitempty"`
}

// facetJSON lists the values of one sidebar field with their counts and
// whether the query keeps them.
type facetJSON struct {
	Field  string           `json:"field"`
	Mode   string           `json:"mode"`
	Values []facetValueJSON `json:"values"`
}

type facetValueJSON struct {
	Value    string `json:"value"`
	Count    int    `json:"count"`
	Selected bool   `json:"selected"`
	// Running marks services with a task running in Nomad now.
	Running bool `json:"running,omitempty"`
}

// queryResponse answers /api/query. The first page (no cursor) also
// carries the total, the histogram, and the facets; later pages only add
// rows.
type queryResponse struct {
	Rows       []rowJSON      `json:"rows"`
	NextCursor string         `json:"next_cursor,omitempty"`
	Total      *int           `json:"total,omitempty"`
	Histogram  *histogramJSON `json:"histogram,omitempty"`
	Facets     []facetJSON    `json:"facets,omitempty"`
	// MaxID is the newest row id when the query ran; live updates
	// resume after it.
	MaxID int64 `json:"max_id"`
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	filters, err := filtersFromRequest(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	filters.Limit, err = intParam(r, "limit", defaultPageSize, 1, maxPageSize)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	ctx := r.Context()
	// Read the newest id first: a row inserted while the query runs is
	// then delivered by live updates at worst twice, never missed.
	maxID, err := s.store.MaxID(ctx)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	entries, err := s.store.Search(ctx, filters)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	response := queryResponse{Rows: toRows(entries), MaxID: maxID}
	if len(entries) == filters.Limit {
		response.NextCursor = store.CursorAfter(entries[len(entries)-1]).String()
	}
	if filters.After == nil {
		h, err := s.store.Histogram(ctx, filters, histogramBins)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		response.Total = &h.Total
		response.Histogram = &histogramJSON{
			StartMS: h.Start.UnixMilli(),
			EndMS:   h.End.UnixMilli(),
			Bins:    make([]histogramBinJSON, 0, len(h.Bins)),
		}
		for _, bin := range h.Bins {
			response.Histogram.Bins = append(response.Histogram.Bins, histogramBinJSON{Count: bin.Count, Levels: bin.Levels})
		}
		if response.Facets, err = s.facets(ctx, filters); err != nil {
			writeJSONError(w, err)
			return
		}
	}
	writeJSON(w, response)
}

// facets lists services and level buckets with their counts. Services
// combine those with stored rows and those running in Nomad now, plus
// any the query names, so the sidebar works while Nomad is unreachable.
func (s *Server) facets(ctx context.Context, filters store.SearchFilters) ([]facetJSON, error) {
	counts, err := s.store.FacetCounts(ctx, filters)
	if err != nil {
		return nil, err
	}

	running := s.ingest.visibleServices()
	facets := make([]facetJSON, 0, 2)
	for _, field := range []string{"service", "level"} {
		sel, err := store.SelectionOf(filters.Query, field)
		if err != nil {
			return nil, err
		}

		byValue := make(map[string]int)
		var values []string
		add := func(value string, count int) {
			if _, ok := byValue[value]; !ok {
				values = append(values, value)
			}
			byValue[value] += count
		}
		for _, c := range counts[field] {
			add(c.Value, c.Count)
		}
		if field == "service" {
			for _, service := range running {
				add(service, 0)
			}
			if sel.Mode == store.SelectInclude || sel.Mode == store.SelectExclude {
				for _, value := range sel.Values {
					add(value, 0)
				}
			}
			slices.Sort(values)
		}

		facet := facetJSON{Field: field, Mode: string(sel.Mode), Values: make([]facetValueJSON, 0, len(values))}
		for _, value := range values {
			facet.Values = append(facet.Values, facetValueJSON{
				Value:    value,
				Count:    byValue[value],
				Selected: isSelected(sel, value),
				Running:  field == "service" && slices.Contains(running, value),
			})
		}
		facets = append(facets, facet)
	}
	return facets, nil
}

func isSelected(sel store.Selection, value string) bool {
	switch sel.Mode {
	case store.SelectAll:
		return true
	case store.SelectInclude:
		return slices.Contains(sel.Values, value)
	case store.SelectExclude:
		return !slices.Contains(sel.Values, value)
	default:
		return false
	}
}

// handleLive streams rows matching q as they are stored, over SSE. Each
// "rows" event carries a JSON array, oldest first, and the id of its last
// row, so a reconnecting EventSource resumes through Last-Event-ID. The
// first connection starts after the "after" parameter, normally the
// max_id of the query that filled the page.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	filters, err := filtersFromRequest(r)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := store.ValidateQuery(filters.Query); err != nil {
		writeJSONError(w, err)
		return
	}
	// Live rows are new by definition; a time window would only hide them.
	filters.Since, filters.Until = time.Time{}, time.Time{}
	filters.Limit = maxPageSize

	resume := r.Header.Get("Last-Event-ID")
	if resume == "" {
		resume = r.URL.Query().Get("after")
	}
	lastID, err := strconv.ParseInt(resume, 10, 64)
	if err != nil || lastID < 0 {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "after must be a row id"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := fmt.Fprint(w, "retry: 2000\n\n"); err != nil {
		return
	}
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		entries, err := s.store.SearchAfter(ctx, lastID, filters)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Printf("warning: live query: %v\n", err)
			}
			return
		}
		if len(entries) == 0 {
			continue
		}
		payload, err := json.Marshal(toRows(entries))
		if err != nil {
			fmt.Printf("warning: encode live rows: %v\n", err)
			return
		}
		lastID = entries[len(entries)-1].ID
		if _, err := fmt.Fprintf(w, "id: %d\nevent: rows\ndata: %s\n\n", lastID, payload); err != nil {
			return
		}
		flusher.Flush()
	}
}

// handleSelect rewrites q so a facet field keeps the given values; the
// sidebar uses it so checkbox clicks edit the query text with the same
// parser that runs it.
func (s *Server) handleSelect(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	sel := store.Selection{Mode: store.SelectionMode(params.Get("mode")), Values: params["value"]}
	if _, ok := store.FacetFields[params.Get("field")]; !ok {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "field must be service or level"})
		return
	}
	switch sel.Mode {
	case store.SelectAll, store.SelectInclude, store.SelectExclude:
	default:
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "mode must be all, include, or exclude"})
		return
	}
	query, err := store.SetSelection(params.Get("q"), params.Get("field"), sel)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSON(w, map[string]string{"q": query})
}

// handleFilter narrows q to rows where a field matches (or, with
// exclude=1, doesn't match) a value.
func (s *Server) handleFilter(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	query, err := store.AddFilter(params.Get("q"), params.Get("field"), params.Get("value"), params.Get("exclude") == "1")
	if err != nil {
		var queryErr *store.QueryError
		if errors.As(err, &queryErr) {
			writeJSONError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"q": query})
}

// handleContext returns the lines around one row from the same task
// stream, in log order.
func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "id must be a row id"})
		return
	}
	n, err := intParam(r, "n", 50, 1, maxContextLines)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	entries, err := s.store.Around(r.Context(), id, n)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	if len(entries) == 0 {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "row not found; it may have been pruned"})
		return
	}
	writeJSON(w, map[string]any{"rows": toRows(entries), "anchor": id})
}

// intParam parses an optional integer query parameter within [lo, hi].
func intParam(r *http.Request, name string, fallback, lo, hi int) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s must be a number from %d to %d", name, lo, hi)
	}
	return n, nil
}
