package server

import (
	"encoding/json"
	"fmt"
	"net/http"
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

// queryResponse answers /api/query. The first page (no cursor) also
// carries the total and the histogram; later pages only add rows.
type queryResponse struct {
	Rows       []rowJSON      `json:"rows"`
	NextCursor string         `json:"next_cursor,omitempty"`
	Total      *int           `json:"total,omitempty"`
	Histogram  *histogramJSON `json:"histogram,omitempty"`
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
	}
	writeJSON(w, response)
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
