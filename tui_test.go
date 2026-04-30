package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

func TestHighlightJSONLogLineSkipsLogPrefix(t *testing.T) {
	r := require.New(t)
	lipgloss.SetColorProfile(termenv.ANSI256)

	line := logLineStyle.Render("[alloc task stdout]") + ` {"level":"info","ok":true,"count":3}`
	got := highlightJSONLogLine(line)

	r.Equal(`[alloc task stdout] {"level":"info","ok":true,"count":3}`, ansi.Strip(got))
	r.NotContains(ansi.Strip(got), "[0m")
	r.NotEqual(line, got)
	r.Contains(got, "\x1b[")
}

func TestStyledByteOffsetSkipsFullCSISequences(t *testing.T) {
	r := require.New(t)

	styled := "\x1b[38;5;75m[prefix]\x1b[0m " + `{"msg":"ok"}`
	plain := ansi.Strip(styled)
	plainOffset := strings.Index(plain, "{")
	styledOffset := styledByteOffset(styled, plainOffset)

	r.Equal(strings.Index(styled, "{"), styledOffset)
}

func TestHighlightJSONLogLineLeavesNonJSONAlone(t *testing.T) {
	r := require.New(t)

	line := logLineStyle.Render("[alloc task stdout]") + " definitely not json"
	r.Equal(line, highlightJSONLogLine(line))
}

func TestLogLevelPrefix(t *testing.T) {
	r := require.New(t)
	lipgloss.SetColorProfile(termenv.ANSI256)

	tests := map[string]struct {
		line string
		want string
	}{
		"debug": {
			line: `{"level":"debug","msg":"details"}`,
			want: "DEBUG",
		},
		"info": {
			line: `{"level":"info","msg":"ready"}`,
			want: "INFO ",
		},
		"warn": {
			line: `{"level":"warning","msg":"slow"}`,
			want: "WARN ",
		},
		"error": {
			line: `{"level":"ERROR","msg":"failed"}`,
			want: "ERROR",
		},
		"crit": {
			line: `{"level":"fatal","msg":"crashed"}`,
			want: "CRIT ",
		},
		"missing": {
			line: `{"msg":"no level"}`,
			want: "     ",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := logLevelPrefix(tc.line)
			r.Equal(tc.want, ansi.Strip(got))
			r.Len(ansi.Strip(got), 5)
		})
	}

	r.Contains(logLevelPrefix(`{"level":"info"}`), "\x1b[")
}

func TestAppendLinePrependsPaddedLogLevel(t *testing.T) {
	r := require.New(t)
	lipgloss.SetColorProfile(termenv.ANSI256)

	app := app{config: appConfig{maxLines: 10}}
	app.appendLine(logLine{
		Source: logSource{
			AllocID: "abcdef123456",
			Task:    "web",
			Stream:  "stdout",
		},
		Text: `{"level":"warn","msg":"slow"}`,
	})

	r.Len(app.logBuffer, 1)
	r.Equal(`WARN  [abcdef12 web stdout] {"level":"warn","msg":"slow"}`, ansi.Strip(app.logBuffer[0]))
	r.Contains(app.logBuffer[0], "\x1b[")
}

func TestRenderedLogsCacheFiltersAndAppends(t *testing.T) {
	r := require.New(t)

	keepOne := `[alloc task stderr] {"msg":"keep one"}`
	drop := `[alloc task stderr] {"msg":"drop"}`
	keepTwo := `[alloc task stderr] {"msg":"keep two"}`
	app := app{
		config:      appConfig{maxLines: 10},
		searchQuery: "@msg:/keep/",
	}

	app.appendRawLine(keepOne)
	r.Equal([]string{keepOne}, app.renderedLogs())
	r.True(app.searchCacheValid)
	r.True(app.renderCacheValid)
	r.Equal("@msg:/keep/", app.searchCacheQuery)

	app.appendRawLine(drop)
	r.Equal([]string{keepOne}, app.renderedLogs())
	r.Equal(2, app.renderCacheLen)

	app.appendRawLine(keepTwo)
	r.Equal([]string{keepOne, keepTwo}, app.renderedLogs())
	r.Equal(3, app.renderCacheLen)
}

func TestSetSearchQueryInvalidatesSearchAndRenderCaches(t *testing.T) {
	r := require.New(t)

	keep := `[alloc task stderr] {"msg":"keep"}`
	drop := `[alloc task stderr] {"msg":"drop"}`
	app := app{
		config:      appConfig{maxLines: 10},
		searchQuery: "@msg:/keep/",
		logBuffer:   []string{keep, drop},
	}

	r.Equal([]string{keep}, app.renderedLogs())
	r.True(app.searchCacheValid)
	r.True(app.renderCacheValid)

	app.setSearchQuery("@msg:/drop/")

	r.False(app.searchCacheValid)
	r.False(app.renderCacheValid)
	r.Equal([]string{drop}, app.renderedLogs())
	r.Equal("@msg:/drop/", app.searchCacheQuery)
}

func TestRenderedLogsCacheInvalidatesForJSONHighlight(t *testing.T) {
	r := require.New(t)
	lipgloss.SetColorProfile(termenv.ANSI256)

	line := `[alloc task stderr] {"msg":"keep","ok":true}`
	app := app{
		config:        appConfig{maxLines: 10},
		highlightJSON: false,
		logBuffer:     []string{line},
	}

	r.Equal([]string{line}, app.renderedLogs())
	r.True(app.renderCacheValid)

	app.highlightJSON = true
	app.invalidateRenderCache()

	got := app.renderedLogs()
	r.Len(got, 1)
	r.Equal(line, ansi.Strip(got[0]))
	r.Contains(got[0], "\x1b[")
}

func TestAppendRawLineUpdatesRenderCacheForEviction(t *testing.T) {
	r := require.New(t)

	keepOne := `[alloc task stderr] {"msg":"keep one"}`
	drop := `[alloc task stderr] {"msg":"drop"}`
	keepTwo := `[alloc task stderr] {"msg":"keep two"}`
	app := app{
		config:      appConfig{maxLines: 2},
		searchQuery: "@msg:/keep/",
	}

	app.appendRawLine(keepOne)
	app.appendRawLine(drop)
	r.Equal([]string{keepOne}, app.renderedLogs())

	app.appendRawLine(keepTwo)

	r.Equal([]string{drop, keepTwo}, app.logBuffer)
	r.Equal([]string{keepTwo}, app.renderedLogs())
	r.Equal(2, app.renderCacheLen)
}

func TestSearchHistoryNavigation(t *testing.T) {
	r := require.New(t)

	search := textinput.New()
	search.SetValue("@msg:/draft/")
	app := app{
		search:           search,
		searchQuery:      "@msg:/draft/",
		searchHistory:    []string{"@level:error", "@msg:/healthy/"},
		searchHistoryPos: -1,
	}

	app.previousSearch()
	r.Equal("@level:error", app.search.Value())
	r.Equal("@level:error", app.searchQuery)

	app.previousSearch()
	r.Equal("@msg:/healthy/", app.search.Value())
	r.Equal("@msg:/healthy/", app.searchQuery)

	app.previousSearch()
	r.Equal("@msg:/healthy/", app.search.Value())

	app.nextSearch()
	r.Equal("@level:error", app.search.Value())

	app.nextSearch()
	r.Equal("@msg:/draft/", app.search.Value())
	r.Equal("@msg:/draft/", app.searchQuery)

	app.nextSearch()
	r.Equal("@msg:/draft/", app.search.Value())
}

func TestRememberSearchDedupesAndTrims(t *testing.T) {
	r := require.New(t)

	app := app{
		searchHistory: []string{"@level:error", "@msg:/healthy/"},
	}

	app.rememberSearch(" @msg:/healthy/ ")

	r.Equal([]string{"@msg:/healthy/", "@level:error"}, app.searchHistory)
}

func TestNewAppDefaultsToStderrLogs(t *testing.T) {
	r := require.New(t)

	app := newApp(nil, appConfig{}, nil)

	r.Equal("stderr", app.config.logType)
	r.True(app.follow)
	r.True(app.highlightJSON)
	r.False(app.wrapLogs)
}

func TestServiceItemTitleIncludesStatsInline(t *testing.T) {
	r := require.New(t)

	item := serviceItem{service: serviceSummary{
		Name:      "api",
		Source:    "service",
		Type:      "tcp",
		Status:    "passing",
		Provider:  "nomad",
		Instances: 3,
		Tags:      []string{"public", "blue"},
	}}

	r.Equal("api  3 registrations | service | tcp | passing | provider nomad | tags public, blue", item.Title())
	r.Equal("3 registrations | service | tcp | passing | provider nomad | tags public, blue", item.Description())
}

func TestServiceItemTitleUsesRunningTasksForJobs(t *testing.T) {
	r := require.New(t)

	item := serviceItem{service: serviceSummary{
		Name:      "worker",
		Source:    "job",
		Instances: 2,
	}}

	r.Equal("worker  2 running tasks | job", item.Title())
}

func TestServiceItemTitlePadsNamesToSharedWidth(t *testing.T) {
	r := require.New(t)

	services := []serviceSummary{
		{Name: "api", Instances: 1},
		{Name: "billing-worker", Instances: 2},
	}
	nameWidth := serviceNameWidth(services)
	short := serviceItem{service: services[0], nameWidth: nameWidth}
	long := serviceItem{service: services[1], nameWidth: nameWidth}

	r.Equal(strings.Index(long.Title(), "2 registrations"), strings.Index(short.Title(), "1 registrations"))
}

func TestServiceItemTitlePadsStatsToSharedColumns(t *testing.T) {
	r := require.New(t)

	services := []serviceSummary{
		{Name: "api", Source: "job", Status: "running", Instances: 3},
		{Name: "billing-worker", Source: "job", Status: "running", Instances: 17},
	}
	nameWidth := serviceNameWidth(services)
	statWidths := serviceStatColumnWidths(services)
	short := serviceItem{service: services[0], nameWidth: nameWidth, statWidths: statWidths}
	long := serviceItem{service: services[1], nameWidth: nameWidth, statWidths: statWidths}

	r.Equal(strings.Index(long.Title(), "running tasks"), strings.Index(short.Title(), "running tasks"))
	r.Equal(strings.Index(long.Title(), "job"), strings.Index(short.Title(), "job"))
	r.Equal(strings.LastIndex(long.Title(), "running"), strings.LastIndex(short.Title(), "running"))
}

func TestServiceDelegateUsesCompactRows(t *testing.T) {
	r := require.New(t)

	delegate := newServiceDelegate()

	r.Equal(1, delegate.Height())
	r.Zero(delegate.Spacing())
}

func TestServiceDelegateRendersColoredAlignedStats(t *testing.T) {
	r := require.New(t)
	lipgloss.SetColorProfile(termenv.ANSI256)

	services := []serviceSummary{
		{Name: "api", Source: "service", Type: "tcp", Status: "passing", Instances: 1},
		{Name: "billing-worker", Source: "job", Status: "running", Instances: 2},
	}
	nameWidth := serviceNameWidth(services)
	statWidths := serviceStatColumnWidths(services)
	item := serviceItem{service: services[0], nameWidth: nameWidth, statWidths: statWidths}
	delegate := newServiceDelegate()
	model := list.New([]list.Item{item}, delegate, 100, 10)

	var rendered strings.Builder
	delegate.Render(&rendered, model, 0, item)

	r.Contains(rendered.String(), "\x1b[")
	plain := ansi.Strip(rendered.String())
	r.Contains(plain, "> api")
	r.Equal(2+nameWidth+2, strings.Index(plain, "1 registrations"))
	r.Contains(plain, "service | tcp | passing")
}

func TestNewAppAppliesPreferences(t *testing.T) {
	r := require.New(t)

	app := newApp(nil, appConfig{
		preferences: appPreferences{
			logType:       "stdout",
			wrapLogs:      true,
			follow:        false,
			highlightJSON: false,
		},
		preferencesSet: true,
	}, nil)

	r.Equal("stdout", app.config.logType)
	r.True(app.wrapLogs)
	r.False(app.follow)
	r.False(app.highlightJSON)
}

func TestSavePreferencesCommandPersistsCurrentToggles(t *testing.T) {
	r := require.New(t)

	store, err := openAppStore(filepath.Join(t.TempDir(), "nomadl.db"))
	r.NoError(err)
	defer store.Close()

	app := app{
		store:         store,
		config:        appConfig{logType: "both"},
		wrapLogs:      true,
		follow:        false,
		highlightJSON: false,
	}

	msg := app.savePreferences()()

	saved, ok := msg.(preferencesSavedMsg)
	r.True(ok)
	r.NoError(saved.err)
	got, err := store.LoadPreferences(defaultAppPreferences())
	r.NoError(err)
	r.Equal(appPreferences{
		logType:       "both",
		wrapLogs:      true,
		follow:        false,
		highlightJSON: false,
	}, got)
}

func TestLogHelpTogglesWithQuestionAndH(t *testing.T) {
	r := require.New(t)

	application := app{screen: screenLogs}

	model, _ := application.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	application = model.(app)
	r.True(application.showHelp)

	model, _ = application.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	application = model.(app)
	r.False(application.showHelp)

	model, _ = application.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	application = model.(app)
	r.True(application.showHelp)

	model, _ = application.Update(tea.KeyMsg{Type: tea.KeyEsc})
	application = model.(app)
	r.False(application.showHelp)
}

func TestLogHelpDoesNotInterceptSearchInput(t *testing.T) {
	r := require.New(t)

	search := textinput.New()
	search.Focus()
	application := app{
		screen:    screenLogs,
		searching: true,
		search:    search,
	}

	model, _ := application.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	application = model.(app)

	r.False(application.showHelp)
	r.Equal("h", application.search.Value())
	r.Equal("h", application.searchQuery)
}

func TestLogsViewUsesCompactHelpPrompt(t *testing.T) {
	r := require.New(t)

	app := newApp(nil, appConfig{}, nil)
	app.screen = screenLogs
	app.selectedService = "api"
	app.width = 100
	app.height = 20
	app.resize()

	view := ansi.Strip(app.logsView())

	r.Contains(view, "?/h: help")
	r.NotContains(view, "esc: services")
	r.NotContains(view, "up/down/pg: vertical")
}

func TestLogsViewRendersHelpPopup(t *testing.T) {
	r := require.New(t)

	app := newApp(nil, appConfig{}, nil)
	app.screen = screenLogs
	app.showHelp = true
	app.selectedService = "api"
	app.width = 100
	app.height = 24
	app.resize()

	view := ansi.Strip(app.logsView())

	r.Contains(view, "Logs")
	r.Contains(view, "s                switch stdout/stderr")
	r.Contains(view, "Search")
	r.Contains(view, "up/ctrl+p        previous search")
	r.Contains(view, "?/h or esc       close help")
}

func TestLogSourcesUseConfiguredStream(t *testing.T) {
	instances := []serviceInstance{
		{AllocID: "alloc-1", JobID: "job-1", Task: "web"},
		{AllocID: "alloc-2", JobID: "job-2", Task: "api"},
	}

	tests := map[string]struct {
		logType string
		want    []string
	}{
		"stderr": {
			logType: "stderr",
			want:    []string{"alloc-1:web:stderr", "alloc-2:api:stderr"},
		},
		"stdout": {
			logType: "stdout",
			want:    []string{"alloc-1:web:stdout", "alloc-2:api:stdout"},
		},
		"both": {
			logType: "both",
			want: []string{
				"alloc-1:web:stdout",
				"alloc-1:web:stderr",
				"alloc-2:api:stdout",
				"alloc-2:api:stderr",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := require.New(t)
			app := app{config: appConfig{logType: tc.logType}}
			gotSources := app.logSources("svc", instances)
			got := make([]string, 0, len(gotSources))
			for _, source := range gotSources {
				got = append(got, source.AllocID+":"+source.Task+":"+source.Stream)
				r.Equal("svc", source.Service)
			}
			r.Equal(tc.want, got)
		})
	}
}

func TestNextLogStream(t *testing.T) {
	r := require.New(t)

	r.Equal("stdout", nextLogStream("stderr"))
	r.Equal("stderr", nextLogStream("stdout"))
	r.Equal("stderr", nextLogStream("both"))
}

func TestToggleLogStreamClearsCurrentLogs(t *testing.T) {
	r := require.New(t)

	app := app{
		config:           appConfig{logType: "stderr"},
		logBuffer:        []string{"old"},
		renderCache:      []string{"old"},
		renderCacheValid: true,
		lineCount:        1,
		lastError:        "previous error",
	}

	command := app.toggleLogStream()

	r.Nil(command)
	r.Equal("stdout", app.config.logType)
	r.Empty(app.logBuffer)
	r.False(app.renderCacheValid)
	r.Zero(app.lineCount)
	r.Empty(app.lastError)
}

func TestReconcileLogStreamsCancelsRemovedStreams(t *testing.T) {
	r := require.New(t)

	source := logSource{Service: "svc", AllocID: "alloc-1", Task: "web", Stream: "stderr"}
	canceled := false
	app := app{
		config:      appConfig{logType: "stderr"},
		logMessages: make(chan tea.Msg, 1),
		activeLogStreams: map[string]context.CancelFunc{
			logSourceKey(source): func() { canceled = true },
		},
	}

	app.reconcileLogStreams("svc", nil)

	r.True(canceled)
	r.Empty(app.activeLogStreams)
	r.Empty(app.selectedInstances)
}

func TestReconcileLogStreamsKeepsExistingDesiredStreams(t *testing.T) {
	r := require.New(t)

	source := logSource{Service: "svc", AllocID: "alloc-1", Task: "web", Stream: "stderr"}
	canceled := false
	app := app{
		config:      appConfig{logType: "stderr"},
		logMessages: make(chan tea.Msg, 1),
		activeLogStreams: map[string]context.CancelFunc{
			logSourceKey(source): func() { canceled = true },
		},
	}

	app.reconcileLogStreams("svc", []serviceInstance{{AllocID: "alloc-1", Task: "web"}})

	r.False(canceled)
	r.Len(app.activeLogStreams, 1)
	r.Equal([]serviceInstance{{AllocID: "alloc-1", Task: "web"}}, app.selectedInstances)
}

func TestJSONStartOffset(t *testing.T) {
	r := require.New(t)

	tests := map[string]struct {
		line string
		want int
	}{
		"prefixed object": {
			line: `[alloc task stdout] {"message":"Troy and Abed in the morning"}`,
			want: strings.Index(`[alloc task stdout] {"message":"Troy and Abed in the morning"}`, "{"),
		},
		"prefixed array": {
			line: `[alloc task stdout] [{"dean":"Pelton"}]`,
			want: strings.Index(`[alloc task stdout] [{"dean":"Pelton"}]`, "[{"),
		},
		"none": {
			line: `[alloc task stdout] paintball episode`,
			want: -1,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r.Equal(tc.want, jsonStartOffset(tc.line))
		})
	}
}

func TestJSONRangeAllowsTrailingJunk(t *testing.T) {
	r := require.New(t)

	line := `[alloc task stdout][0m {"@message":"query returned no healthy instances for service"} trailing junk`
	start, end := jsonRange(line)

	r.Equal(strings.Index(line, "{"), start)
	r.Equal(start+len(`{"@message":"query returned no healthy instances for service"}`), end)
}

func TestLogMatchesSearch(t *testing.T) {
	line := `[alloc task stdout][0m {"@message":"query returned no healthy instances for service","error":"foo-baz-bar","level":"error","count":3,"ok":false,"grpc.status_code":"OK","grpc":{"status_code":"BAD"},"nested":{"code":"E_DEAN"}} trailing junk`

	tests := map[string]struct {
		query string
		want  bool
	}{
		"text search": {
			query: "dean",
			want:  true,
		},
		"negated text search excludes matches": {
			query: "-healthy",
			want:  false,
		},
		"negated text search includes mismatches": {
			query: "-paintball",
			want:  true,
		},
		"string field match": {
			query: "@error:foo-baz-bar",
			want:  true,
		},
		"string field contains wildcard match": {
			query: "@error:/baz/",
			want:  true,
		},
		"at-prefixed key contains wildcard match": {
			query: "@message:/healthy/",
			want:  true,
		},
		"at-prefixed key leading wildcard match": {
			query: "@message:/*healthy*/",
			want:  true,
		},
		"at-prefixed key ordered wildcard match": {
			query: "@message:/query*/",
			want:  true,
		},
		"msg pseudo field falls back to message": {
			query: "@msg:/healthy/",
			want:  true,
		},
		"negated msg pseudo field excludes matches": {
			query: "-@msg:/healthy/",
			want:  false,
		},
		"negated msg pseudo field includes mismatches": {
			query: "-@msg:/paintball/",
			want:  true,
		},
		"negated missing field includes line": {
			query: "-@missing:foo",
			want:  true,
		},
		"err pseudo field falls back to error": {
			query: "@err:/foo-*-bar/",
			want:  true,
		},
		"string field wildcard match": {
			query: "@error:/foo-*-bar/",
			want:  true,
		},
		"string field wildcard mismatch": {
			query: "@error:/foo-*-dean/",
			want:  false,
		},
		"string field mismatch": {
			query: "@error:bar",
			want:  false,
		},
		"nested field match": {
			query: "@nested.code:E_DEAN",
			want:  true,
		},
		"dotted field name exact key match": {
			query: "@grpc.status_code:OK",
			want:  true,
		},
		"dotted field name prefers exact key over nested path": {
			query: "@grpc.status_code:BAD",
			want:  false,
		},
		"number field match": {
			query: "@count:3",
			want:  true,
		},
		"bool field match": {
			query: "@ok:false",
			want:  true,
		},
		"missing field": {
			query: "@missing:foo",
			want:  false,
		},
		"invalid field query falls back to text": {
			query: "@error",
			want:  false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := require.New(t)
			r.Equal(tc.want, logMatchesSearch(line, tc.query))
		})
	}
}

func TestLogMatchesCompoundSearch(t *testing.T) {
	lines := map[string]string{
		"healthy fail": `[alloc task stdout] {"msg":"healthy service failed over","err":"fail to connect","level":"error","component":"api"} trailing`,
		"healthy ok":   `[alloc task stdout] {"msg":"healthy service ready","err":"","level":"info","component":"web"} trailing`,
		"timeout fail": `[alloc task stderr] {"msg":"timeout waiting for worker","err":"fail after retry","level":"error","component":"worker"} trailing`,
		"plain text":   `[alloc task stdout] foo bar plain log line`,
		"dash text":    `[alloc task stdout] alpha-beta plain log line`,
	}

	tests := map[string]struct {
		query string
		line  string
		want  bool
	}{
		"implicit AND matches both text terms": {
			query: "foo plain",
			line:  "plain text",
			want:  true,
		},
		"implicit AND rejects missing text term": {
			query: "foo missing",
			line:  "plain text",
			want:  false,
		},
		"quoted phrase matches contiguous text": {
			query: `"foo bar"`,
			line:  "plain text",
			want:  true,
		},
		"quoted phrase rejects non-contiguous text": {
			query: `"foo plain"`,
			line:  "plain text",
			want:  false,
		},
		"explicit AND combines text and field": {
			query: `"healthy service" AND @err:/fail/`,
			line:  "healthy fail",
			want:  true,
		},
		"explicit AND rejects when right side misses": {
			query: `"healthy service" AND @err:/fail/`,
			line:  "healthy ok",
			want:  false,
		},
		"OR matches left side": {
			query: `@component:api OR @component:worker`,
			line:  "healthy fail",
			want:  true,
		},
		"OR matches right side": {
			query: `@component:api OR @component:worker`,
			line:  "timeout fail",
			want:  true,
		},
		"OR rejects when neither side matches": {
			query: `@component:api OR @component:worker`,
			line:  "healthy ok",
			want:  false,
		},
		"AND binds tighter than OR": {
			query: `@component:api OR @component:worker AND @msg:/timeout/`,
			line:  "healthy fail",
			want:  true,
		},
		"AND precedence rejects incomplete right branch": {
			query: `@component:api OR @component:worker AND @msg:/timeout/`,
			line:  "healthy ok",
			want:  false,
		},
		"parentheses override precedence": {
			query: `(@component:api OR @component:worker) AND @msg:/timeout/`,
			line:  "healthy fail",
			want:  false,
		},
		"parentheses match grouped branch": {
			query: `(@component:api OR @component:worker) AND @msg:/timeout/`,
			line:  "timeout fail",
			want:  true,
		},
		"NOT operator excludes matches": {
			query: `NOT @level:error`,
			line:  "healthy fail",
			want:  false,
		},
		"NOT operator includes mismatches": {
			query: `NOT @level:error`,
			line:  "healthy ok",
			want:  true,
		},
		"minus negates a field term": {
			query: `-@msg:/healthy/`,
			line:  "healthy ok",
			want:  false,
		},
		"minus negates a parenthesized expression": {
			query: `-(@level:error OR @msg:/healthy/)`,
			line:  "healthy ok",
			want:  false,
		},
		"minus can be combined with implicit AND": {
			query: `@msg:/service/ -@err:/fail/`,
			line:  "healthy ok",
			want:  true,
		},
		"minus implicit AND rejects excluded term": {
			query: `@msg:/service/ -@err:/fail/`,
			line:  "healthy fail",
			want:  false,
		},
		"field value can be quoted": {
			query: `@msg:"healthy service ready"`,
			line:  "healthy ok",
			want:  true,
		},
		"slash pattern can contain spaces": {
			query: `@msg:/timeout waiting/`,
			line:  "timeout fail",
			want:  true,
		},
		"operator words are case-sensitive": {
			query: `foo and bar`,
			line:  "plain text",
			want:  false,
		},
		"operator words can be searched when quoted": {
			query: `"foo bar" "plain log"`,
			line:  "plain text",
			want:  true,
		},
		"bare dash falls back to literal search": {
			query: `-`,
			line:  "dash text",
			want:  true,
		},
		"incomplete expression falls back to literal search": {
			query: `@msg:/timeout/ OR`,
			line:  "timeout fail",
			want:  false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := require.New(t)
			r.Equal(tc.want, logMatchesSearch(lines[tc.line], tc.query))
		})
	}
}

func TestWildcardContains(t *testing.T) {
	r := require.New(t)

	tests := map[string]struct {
		value   string
		pattern string
		want    bool
	}{
		"contains": {
			value:   "the foo message",
			pattern: "foo",
			want:    true,
		},
		"ordered gaps": {
			value:   "foo-Greendale-bar",
			pattern: "foo-*-bar",
			want:    true,
		},
		"ordered mismatch": {
			value:   "bar-Greendale-foo",
			pattern: "foo-*-bar",
			want:    false,
		},
		"case insensitive": {
			value:   "Troy And Abed",
			pattern: "troy*abed",
			want:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r.Equal(tc.want, wildcardContains(tc.value, tc.pattern))
		})
	}
}

func TestJSONPseudoFieldsPreferExactFields(t *testing.T) {
	r := require.New(t)

	line := `[alloc task stdout] {"msg":"short","@message":"long","err":"small","error":"large"}`

	r.True(logMatchesSearch(line, "@msg:short"))
	r.False(logMatchesSearch(line, "@msg:long"))
	r.True(logMatchesSearch(line, "@err:small"))
	r.False(logMatchesSearch(line, "@err:large"))
}

func TestParseJSONFieldSearch(t *testing.T) {
	r := require.New(t)

	field, value, ok := parseJSONFieldSearch("@error:foo")
	r.True(ok)
	r.Equal("error", field)
	r.Equal("foo", value)

	_, _, ok = parseJSONFieldSearch("error:foo")
	r.False(ok)
}

func TestTokenizeLogSearch(t *testing.T) {
	tests := map[string]struct {
		query string
		want  []searchToken
	}{
		"operators and grouping": {
			query: `"foo bar" AND (@err:/fail/ OR -@msg:/healthy/)`,
			want: []searchToken{
				{kind: searchTokenText, value: "foo bar"},
				{kind: searchTokenAnd, value: "AND"},
				{kind: searchTokenLeftParen, value: "("},
				{kind: searchTokenText, value: "@err:/fail/"},
				{kind: searchTokenOr, value: "OR"},
				{kind: searchTokenNot, value: "-"},
				{kind: searchTokenText, value: "@msg:/healthy/"},
				{kind: searchTokenRightParen, value: ")"},
			},
		},
		"quoted field value": {
			query: `@msg:"healthy service"`,
			want: []searchToken{
				{kind: searchTokenText, value: "@msg:healthy service"},
			},
		},
		"slash pattern with spaces": {
			query: `@msg:/healthy service/`,
			want: []searchToken{
				{kind: searchTokenText, value: "@msg:/healthy service/"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := require.New(t)
			got, err := tokenizeLogSearch(tc.query)
			r.NoError(err)
			r.Equal(tc.want, got)
		})
	}
}

func TestParseLogSearchRejectsInvalidSyntax(t *testing.T) {
	tests := []string{
		`foo AND`,
		`foo OR`,
		`NOT`,
		`(`,
		`)`,
		`"unterminated`,
		`@msg:/unterminated`,
	}

	for _, query := range tests {
		t.Run(query, func(t *testing.T) {
			r := require.New(t)
			_, err := parseLogSearch(query)
			r.Error(err)
		})
	}
}
