package store

import (
	"errors"
	"slices"
	"testing"
)

func TestSelectionOf(t *testing.T) {
	tests := map[string]struct {
		query string
		field string
		want  Selection
	}{
		"no clause":                {query: `timeout`, field: "service", want: Selection{Mode: SelectAll}},
		"single value":             {query: `service:api timeout`, field: "service", want: Selection{Mode: SelectInclude, Values: []string{"api"}}},
		"alias":                    {query: `job:api`, field: "service", want: Selection{Mode: SelectInclude, Values: []string{"api"}}},
		"group":                    {query: `service:(api OR web)`, field: "service", want: Selection{Mode: SelectInclude, Values: []string{"api", "web"}}},
		"explicit or":              {query: `service:api OR service:web`, field: "service", want: Selection{Mode: SelectInclude, Values: []string{"api", "web"}}},
		"quoted value":             {query: `service:"study room"`, field: "service", want: Selection{Mode: SelectInclude, Values: []string{"study room"}}},
		"exclusion":                {query: `-service:api`, field: "service", want: Selection{Mode: SelectExclude, Values: []string{"api"}}},
		"not keyword":              {query: `NOT service:(api OR web)`, field: "service", want: Selection{Mode: SelectExclude, Values: []string{"api", "web"}}},
		"none":                     {query: `-service:*`, field: "service", want: Selection{Mode: SelectInclude}},
		"include minus exclude":    {query: `service:(api OR web) -service:web`, field: "service", want: Selection{Mode: SelectInclude, Values: []string{"api"}}},
		"intersection":             {query: `service:(api OR web) service:(web OR db)`, field: "service", want: Selection{Mode: SelectInclude, Values: []string{"web"}}},
		"wildcard is custom":       {query: `service:green*`, field: "service", want: Selection{Mode: SelectCustom}},
		"mixed clause is custom":   {query: `(service:api OR level:error)`, field: "service", want: Selection{Mode: SelectCustom}},
		"other field ignored":      {query: `(service:api OR level:error)`, field: "task", want: Selection{}},
		"level buckets":            {query: `level:(ERR OR warning)`, field: "level", want: Selection{Mode: SelectInclude, Values: []string{"error", "warn"}}},
		"status alias":             {query: `-status:debug`, field: "level", want: Selection{Mode: SelectExclude, Values: []string{"debug"}}},
		"unbucketed raw level":     {query: `level:SEVERE`, field: "level", want: Selection{Mode: SelectInclude, Values: []string{"ok"}}},
		"level comparison custom":  {query: `level:>3`, field: "level", want: Selection{Mode: SelectCustom}},
		"top-level or not a facet": {query: `troy OR service:api`, field: "service", want: Selection{Mode: SelectCustom}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := SelectionOf(tc.query, tc.field)
			if tc.field == "task" {
				if err == nil {
					t.Fatal("want an error for a field that isn't a facet")
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectionOf: %v", err)
			}
			if got.Mode != tc.want.Mode || !slices.Equal(got.Values, tc.want.Values) {
				t.Fatalf("SelectionOf(%q, %s) = %+v, want %+v", tc.query, tc.field, got, tc.want)
			}
		})
	}
}

func TestSetSelection(t *testing.T) {
	tests := map[string]struct {
		query string
		field string
		sel   Selection
		want  string
	}{
		"add to empty":           {query: ``, field: "service", sel: Selection{Mode: SelectInclude, Values: []string{"api"}}, want: `service:api`},
		"append after text":      {query: `"connection refused"`, field: "service", sel: Selection{Mode: SelectInclude, Values: []string{"api", "web"}}, want: `"connection refused" service:(api OR web)`},
		"replace existing":       {query: `service:api timeout -debug`, field: "service", sel: Selection{Mode: SelectInclude, Values: []string{"web"}}, want: `timeout -debug service:web`},
		"replace alias":          {query: `job:(api OR web) timeout`, field: "service", sel: Selection{Mode: SelectExclude, Values: []string{"db"}}, want: `timeout -service:db`},
		"clear to all":           {query: `level:error service:api`, field: "level", sel: Selection{Mode: SelectAll}, want: `service:api`},
		"none":                   {query: `timeout`, field: "service", sel: Selection{Mode: SelectInclude}, want: `timeout -service:*`},
		"explicit and dropped":   {query: `timeout AND service:api AND -debug`, field: "service", sel: Selection{Mode: SelectAll}, want: `timeout -debug`},
		"top-level or wrapped":   {query: `troy OR abed`, field: "level", sel: Selection{Mode: SelectExclude, Values: []string{"debug"}}, want: `(troy OR abed) -level:debug`},
		"group kept intact":      {query: `(timeout AND service:api) level:error`, field: "service", sel: Selection{Mode: SelectInclude, Values: []string{"web"}}, want: `(timeout AND service:api) level:error service:web`},
		"mixed clause kept":      {query: `(service:api OR level:error)`, field: "service", sel: Selection{Mode: SelectInclude, Values: []string{"web"}}, want: `(service:api OR level:error) service:web`},
		"wildcard replaced":      {query: `service:green* timeout`, field: "service", sel: Selection{Mode: SelectInclude, Values: []string{"greendale"}}, want: `timeout service:greendale`},
		"values quoted":          {query: ``, field: "service", sel: Selection{Mode: SelectInclude, Values: []string{"study room", "OR"}}, want: `service:("study room" OR "OR")`},
		"text kept verbatim":     {query: `@user.name:"Troy  Barnes"   paint*`, field: "level", sel: Selection{Mode: SelectInclude, Values: []string{"error"}}, want: `@user.name:"Troy  Barnes" paint* level:error`},
		"empty exclusion is all": {query: `-level:debug`, field: "level", sel: Selection{Mode: SelectExclude}, want: ``},
		"or root removed whole":  {query: `service:api OR service:web`, field: "service", sel: Selection{Mode: SelectAll}, want: ``},
		"negated group removed":  {query: `-service:(api OR web) x`, field: "service", sel: Selection{Mode: SelectAll}, want: `x`},
		"range kept for other":   {query: `@http.status:[500 TO 599]`, field: "level", sel: Selection{Mode: SelectInclude, Values: []string{"error"}}, want: `@http.status:[500 TO 599] level:error`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := SetSelection(tc.query, tc.field, tc.sel)
			if err != nil {
				t.Fatalf("SetSelection: %v", err)
			}
			if got != tc.want {
				t.Fatalf("SetSelection(%q) = %q, want %q", tc.query, got, tc.want)
			}
			if _, err := parseLogQuery(got); err != nil {
				t.Fatalf("rewritten query %q doesn't parse: %v", got, err)
			}
			back, err := SelectionOf(got, tc.field)
			if err != nil {
				t.Fatalf("SelectionOf rewritten: %v", err)
			}
			// A clause mixing fields leaves the selection custom; otherwise
			// reading the rewrite back gives the selection that was set.
			if back.Mode != SelectCustom && len(tc.sel.Values) > 0 && !slices.Equal(back.Values, tc.sel.Values) {
				t.Fatalf("selection after rewrite = %+v, want %+v", back, tc.sel)
			}
		})
	}
}

func TestAddFilter(t *testing.T) {
	tests := map[string]struct {
		query   string
		field   string
		value   string
		exclude bool
		want    string
	}{
		"empty query":      {field: "@dd.trace_id", value: "8a2f", want: `@dd.trace_id:8a2f`},
		"appended":         {query: `level:error`, field: "task", value: "web", want: `level:error task:web`},
		"excluded":         {query: `level:error`, field: "task", value: "web", exclude: true, want: `level:error -task:web`},
		"or root wrapped":  {query: `troy OR abed`, field: "service", value: "api", want: `(troy OR abed) service:api`},
		"value with space": {field: "@user.name", value: `Troy "T-Bone" Barnes`, want: `@user.name:"Troy \"T-Bone\" Barnes"`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := AddFilter(tc.query, tc.field, tc.value, tc.exclude)
			if err != nil {
				t.Fatalf("AddFilter: %v", err)
			}
			if got != tc.want {
				t.Fatalf("AddFilter = %q, want %q", got, tc.want)
			}
		})
	}

	var queryErr *QueryError
	if _, err := AddFilter(`"unterminated`, "task", "web", false); !errors.As(err, &queryErr) {
		t.Fatalf("AddFilter on a bad query = %v, want *QueryError", err)
	}
	if _, err := AddFilter(`x`, "bad field", "web", false); err == nil {
		t.Fatal("AddFilter accepted a field with a space")
	}
}

func TestQuoteValueRoundTrips(t *testing.T) {
	values := []string{"api", "study room", "OR", "-negative", "a:b", `back\slash`, `"quoted"`, "wild*", "[range", ">5", ""}
	for _, value := range values {
		root, err := parseLogQuery("service:" + QuoteValue(value))
		if err != nil {
			t.Fatalf("QuoteValue(%q) = %q doesn't parse: %v", value, QuoteValue(value), err)
		}
		if root.kind != queryNodeTerm || root.value != value || root.field != "service" {
			t.Fatalf("QuoteValue(%q) = %q parses as %+v", value, QuoteValue(value), root)
		}
	}
}

func TestHighlightTerms(t *testing.T) {
	tests := map[string]struct {
		query string
		want  []string
	}{
		"empty":             {query: ``, want: nil},
		"bare terms":        {query: `timeout retry`, want: []string{"timeout", "retry"}},
		"phrase":            {query: `"connection refused" service:api`, want: []string{"connection refused"}},
		"wildcard pieces":   {query: `paint*tour`, want: []string{"paint", "tour"}},
		"negation skipped":  {query: `error -health NOT "ping ok"`, want: []string{"error"}},
		"double negation":   {query: `-(-annie)`, want: []string{"annie"}},
		"message fields":    {query: `message:abed raw:troy *:britta task:web`, want: []string{"abed", "troy", "britta"}},
		"invalid query":     {query: `"unterminated`, want: nil},
		"existence skipped": {query: `message:*`, want: nil},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := HighlightTerms(tc.query); !slices.Equal(got, tc.want) {
				t.Fatalf("HighlightTerms(%q) = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}
