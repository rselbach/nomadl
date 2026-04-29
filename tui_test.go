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
