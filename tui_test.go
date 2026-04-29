package main

import (
	"strings"
	"testing"

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

	r.Contains(ansi.Strip(got), `{"level":"info","ok":true,"count":3}`)
	r.NotEqual(line, got)
	r.Contains(got, "\x1b[")
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
	line := `[alloc task stdout][0m {"@message":"query returned no healthy instances for service","error":"foo-baz-bar","level":"error","count":3,"ok":false,"nested":{"code":"E_DEAN"}} trailing junk`

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
