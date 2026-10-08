package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rselbach/nomadl/internal/appconfig"
	"github.com/rselbach/nomadl/internal/store"
)

func getJSON(t *testing.T, url string, wantStatus int, into any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s status = %d, want %d: %s", url, resp.StatusCode, wantStatus, body)
	}
	if into != nil {
		if err := json.Unmarshal(body, into); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}
}

func newAPITestServer(t *testing.T, entries []store.LogEntry) (*Server, string) {
	t.Helper()
	srv := newTestServer(t)
	if err := srv.store.InsertLogs(entries); err != nil {
		t.Fatalf("insert logs: %v", err)
	}
	ts := httptest.NewServer(srv.mux)
	t.Cleanup(ts.Close)
	return srv, ts.URL
}

func TestQueryPagesRowsWithHistogramOnFirstPage(t *testing.T) {
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	levels := []string{"INFO", "ERR", "INFO", "WARN", "ERROR"}
	var entries []store.LogEntry
	for i, level := range levels {
		entries = append(entries, store.LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Job:       "greendale", AllocID: "alloc-1", Task: "web", Stream: "stderr",
			Level:   level,
			Message: fmt.Sprintf("Troy Barnes %d", i),
			Raw:     fmt.Sprintf(`{"msg":"Troy Barnes %d"}`, i),
			LineRef: fmt.Sprintf("f@%d", i),
		})
	}
	_, url := newAPITestServer(t, entries)

	var first queryResponse
	getJSON(t, url+"/api/query?limit=2", http.StatusOK, &first)
	if len(first.Rows) != 2 || first.Rows[0].Message != "Troy Barnes 4" || first.Rows[1].Message != "Troy Barnes 3" {
		t.Fatalf("first page = %+v, want the two newest rows", first.Rows)
	}
	if first.Rows[0].LevelBucket != "error" || first.Rows[0].Raw == "" || first.Rows[0].TimeMS != base.Add(4*time.Second).UnixMilli() {
		t.Fatalf("row = %+v, want error bucket, raw payload, and timestamp", first.Rows[0])
	}
	if first.Total == nil || *first.Total != 5 || first.Histogram == nil || len(first.Histogram.Bins) != histogramBins {
		t.Fatalf("first page total/histogram = %v/%v, want 5 and %d bins", first.Total, first.Histogram, histogramBins)
	}
	if first.NextCursor == "" || first.MaxID != 5 {
		t.Fatalf("cursor = %q max id = %d, want a cursor and 5", first.NextCursor, first.MaxID)
	}

	var second queryResponse
	getJSON(t, url+"/api/query?limit=2&cursor="+first.NextCursor, http.StatusOK, &second)
	if len(second.Rows) != 2 || second.Rows[0].Message != "Troy Barnes 2" {
		t.Fatalf("second page = %+v, want rows 2 and 1", second.Rows)
	}
	if second.Total != nil || second.Histogram != nil {
		t.Fatal("later pages must not repeat the total and histogram")
	}

	var errors queryResponse
	getJSON(t, url+"/api/query?q=level:error", http.StatusOK, &errors)
	if len(errors.Rows) != 2 || *errors.Total != 2 || errors.NextCursor != "" {
		t.Fatalf("level:error = %d rows, total %v, cursor %q; want 2, 2, none", len(errors.Rows), errors.Total, errors.NextCursor)
	}

	var bad map[string]any
	getJSON(t, url+"/api/query?q=troy+%22abed", http.StatusBadRequest, &bad)
	if bad["pos"] != float64(5) {
		t.Fatalf("error body = %v, want pos 5", bad)
	}
	getJSON(t, url+"/api/query?limit=0", http.StatusBadRequest, nil)
	getJSON(t, url+"/api/query?cursor=garbage", http.StatusBadRequest, nil)
}

func TestContextReturnsNeighborsFromTheSameStream(t *testing.T) {
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	var entries []store.LogEntry
	for i := range 6 {
		for _, stream := range []string{"stderr", "stdout"} {
			entries = append(entries, store.LogEntry{
				Timestamp: base.Add(time.Duration(i) * time.Second),
				Job:       "greendale", AllocID: "alloc-1", Task: "web", Stream: stream,
				Message: fmt.Sprintf("%s %d", stream, i),
				LineRef: fmt.Sprintf("%s@%d", stream, i),
			})
		}
	}
	_, url := newAPITestServer(t, entries)

	// Row 5 is "stderr 2"; its stderr neighbors are rows 3, 7 and so on.
	var got struct {
		Rows   []rowJSON `json:"rows"`
		Anchor int64     `json:"anchor"`
	}
	getJSON(t, url+"/api/context?id=5&n=2", http.StatusOK, &got)
	var messages []string
	for _, row := range got.Rows {
		messages = append(messages, row.Message)
	}
	want := []string{"stderr 0", "stderr 1", "stderr 2", "stderr 3", "stderr 4"}
	if fmt.Sprint(messages) != fmt.Sprint(want) || got.Anchor != 5 {
		t.Fatalf("context = %v anchor %d, want %v anchor 5", messages, got.Anchor, want)
	}

	getJSON(t, url+"/api/context?id=999", http.StatusNotFound, nil)
	getJSON(t, url+"/api/context?id=abc", http.StatusBadRequest, nil)
}

// readLiveRows reads "rows" events from an SSE stream until it has want
// rows or times out.
func readLiveRows(t *testing.T, body io.Reader, want int) ([]rowJSON, string) {
	t.Helper()
	type event struct {
		rows []rowJSON
		id   string
	}
	events := make(chan event, 16)
	go func() {
		scanner := bufio.NewScanner(body)
		var id string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				var rows []rowJSON
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &rows); err != nil {
					t.Errorf("decode live rows: %v", err)
					return
				}
				events <- event{rows: rows, id: id}
			}
		}
	}()

	var got []rowJSON
	var lastID string
	deadline := time.After(5 * time.Second)
	for len(got) < want {
		select {
		case e := <-events:
			got = append(got, e.rows...)
			lastID = e.id
		case <-deadline:
			t.Fatalf("got %d live rows, want %d", len(got), want)
		}
	}
	return got, lastID
}

func TestLiveStreamsNewMatchingRowsAndResumes(t *testing.T) {
	entry := func(i int, level string) store.LogEntry {
		return store.LogEntry{
			Timestamp: time.Now(), Job: "greendale", AllocID: "alloc-1", Task: "web", Stream: "stderr",
			Level: level, Message: fmt.Sprintf("Abed Nadir %d", i), LineRef: fmt.Sprintf("f@%d", i),
		}
	}
	srv, url := newAPITestServer(t, []store.LogEntry{entry(0, "ERROR")})

	resp, err := http.Get(url + "/api/live?q=level:error&after=1")
	if err != nil {
		t.Fatalf("live request: %v", err)
	}
	if err := srv.store.InsertLogs([]store.LogEntry{entry(1, "INFO"), entry(2, "ERROR"), entry(3, "ERROR")}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rows, lastID := readLiveRows(t, resp.Body, 2)
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close live stream: %v", err)
	}
	if rows[0].Message != "Abed Nadir 2" || rows[1].Message != "Abed Nadir 3" || lastID != "4" {
		t.Fatalf("live rows = %+v (last id %s), want new error rows 2 and 3, oldest first", rows, lastID)
	}

	// A reconnect resumes after the last delivered row.
	if err := srv.store.InsertLogs([]store.LogEntry{entry(4, "ERROR")}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/api/live?q=level:error&after=0", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Last-Event-ID", lastID)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("resume request: %v", err)
	}
	t.Cleanup(func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close resumed stream: %v", err)
		}
	})
	rows, _ = readLiveRows(t, resp.Body, 1)
	if len(rows) != 1 || rows[0].Message != "Abed Nadir 4" {
		t.Fatalf("resumed rows = %+v, want only row 4", rows)
	}
}

func TestSelectAndFilterRewriteQueries(t *testing.T) {
	_, url := newAPITestServer(t, nil)

	tests := map[string]struct {
		path       string
		wantStatus int
		wantQuery  string
	}{
		"select excludes":   {path: "/api/query/select?q=timeout+service:api&field=level&mode=exclude&value=debug", wantStatus: http.StatusOK, wantQuery: "timeout service:api -level:debug"},
		"select replaces":   {path: "/api/query/select?q=service:api&field=service&mode=include&value=web&value=db", wantStatus: http.StatusOK, wantQuery: "service:(web OR db)"},
		"filter appends":    {path: "/api/query/filter?q=level:error&field=@dd.trace_id&value=8a2f", wantStatus: http.StatusOK, wantQuery: "level:error @dd.trace_id:8a2f"},
		"filter excludes":   {path: "/api/query/filter?q=&field=task&value=web&exclude=1", wantStatus: http.StatusOK, wantQuery: "-task:web"},
		"bad facet field":   {path: "/api/query/select?q=&field=task&mode=all", wantStatus: http.StatusBadRequest},
		"bad mode":          {path: "/api/query/select?q=&field=level&mode=custom", wantStatus: http.StatusBadRequest},
		"unparsable query":  {path: "/api/query/select?q=%22troy&field=level&mode=all", wantStatus: http.StatusBadRequest},
		"bad filter field":  {path: "/api/query/filter?q=&field=a+b&value=x", wantStatus: http.StatusBadRequest},
		"unparsable filter": {path: "/api/query/filter?q=(troy&field=task&value=x", wantStatus: http.StatusBadRequest},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got map[string]any
			getJSON(t, url+tc.path, tc.wantStatus, &got)
			if tc.wantStatus == http.StatusOK && got["q"] != tc.wantQuery {
				t.Fatalf("q = %q, want %q", got["q"], tc.wantQuery)
			}
		})
	}
}

func TestQueryFacetsCountWithoutTheirOwnClauses(t *testing.T) {
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	rows := []struct{ service, level string }{
		{"api", "ERROR"}, {"api", "INFO"}, {"web", "INFO"}, {"web", "DEBUG"},
	}
	var entries []store.LogEntry
	for i, row := range rows {
		entries = append(entries, store.LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second), Job: row.service, AllocID: "a", Task: "t",
			Level: row.level, Message: "Jeff Winger", LineRef: fmt.Sprintf("f@%d", i),
		})
	}
	_, url := newAPITestServer(t, entries)

	var got queryResponse
	getJSON(t, url+"/api/query?q=service:(api+OR+ghost)+-level:debug", http.StatusOK, &got)
	facets := make(map[string]facetJSON)
	for _, facet := range got.Facets {
		facets[facet.Field] = facet
	}

	service := facets["service"]
	wantServices := []facetValueJSON{
		{Value: "api", Count: 2, Selected: true},
		{Value: "ghost", Count: 0, Selected: true},
		{Value: "web", Count: 1, Selected: false},
	}
	if service.Mode != "include" || fmt.Sprint(service.Values) != fmt.Sprint(wantServices) {
		t.Fatalf("service facet = %+v, want include %+v", service, wantServices)
	}

	level := facets["level"]
	counts := make(map[string]int)
	for _, value := range level.Values {
		counts[value.Value] = value.Count
		if value.Selected == (value.Value == "debug") {
			t.Fatalf("level %s selected = %v, want every level but debug", value.Value, value.Selected)
		}
	}
	if level.Mode != "exclude" || len(level.Values) != len(store.LevelBuckets) || counts["error"] != 1 || counts["info"] != 1 || counts["debug"] != 0 {
		t.Fatalf("level facet = %+v, want all buckets counted within service:api", level)
	}
}

func TestSettingsRoundTripTraceFields(t *testing.T) {
	srv, url := newAPITestServer(t, nil)

	var got settingsPayload
	getJSON(t, url+"/api/settings", http.StatusOK, &got)
	if fmt.Sprint(got.TraceFields) != fmt.Sprint(appconfig.DefaultTraceFields) {
		t.Fatalf("default trace fields = %v, want %v", got.TraceFields, appconfig.DefaultTraceFields)
	}

	body := strings.NewReader(`{"ingest_services":["greendale"],"trace_fields":["request.trace"," request.trace ",""]}`)
	resp, err := http.Post(url+"/api/settings", "application/json", body)
	if err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d", resp.StatusCode)
	}

	getJSON(t, url+"/api/settings", http.StatusOK, &got)
	if fmt.Sprint(got.TraceFields) != "[request.trace]" || fmt.Sprint(got.IngestServices) != "[greendale]" {
		t.Fatalf("settings = %+v, want cleaned trace field and allowlist", got)
	}
	saved, err := srv.settingsStore.Load()
	if err != nil {
		t.Fatalf("load saved settings: %v", err)
	}
	if fmt.Sprint(saved.TraceFields) != "[request.trace]" {
		t.Fatalf("saved trace fields = %v", saved.TraceFields)
	}
}
