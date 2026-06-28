package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type QuerySuggestionContext struct {
	Field        string
	Prefix       string
	ReplaceStart int
	ReplaceEnd   int
	ValueMode    bool
	Negated      bool
}

func SuggestionContext(query string, cursor int) QuerySuggestionContext {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(query) {
		cursor = len(query)
	}

	start := suggestionTokenStart(query, cursor)
	end := suggestionTokenEnd(query, cursor)
	tokenBeforeCursor := query[start:cursor]
	negated := strings.HasPrefix(tokenBeforeCursor, "-")
	fieldStart := start
	if negated {
		fieldStart++
	}

	if colon := strings.LastIndex(query[fieldStart:cursor], ":"); colon >= 0 {
		colon += fieldStart
		field := strings.TrimPrefix(query[fieldStart:colon], "@")
		return QuerySuggestionContext{
			Field:        field,
			Prefix:       query[colon+1 : cursor],
			ReplaceStart: colon + 1,
			ReplaceEnd:   end,
			ValueMode:    true,
			Negated:      negated,
		}
	}

	if field, ok := groupedFieldContext(query[:start]); ok {
		valueStart := start
		if negated {
			valueStart++
		}
		return QuerySuggestionContext{
			Field:        field,
			Prefix:       query[valueStart:cursor],
			ReplaceStart: valueStart,
			ReplaceEnd:   end,
			ValueMode:    true,
			Negated:      negated,
		}
	}

	return QuerySuggestionContext{
		Prefix:       query[fieldStart:cursor],
		ReplaceStart: start,
		ReplaceEnd:   end,
		Negated:      negated,
	}
}

func (s *Store) DistinctValues(field, prefix string, limit int) ([]string, error) {
	column, err := suggestionColumn(field)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}

	rows, err := s.db.Query(
		"SELECT DISTINCT "+column+" FROM logs WHERE "+column+" <> '' AND "+column+" LIKE ? ESCAPE '\\' ORDER BY "+column+" LIMIT ?",
		suggestionLikePrefix(prefix),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("distinct %s values: %w", column, err)
	}
	return scanStringRows(rows)
}

func (s *Store) JSONAttributeNames(prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 10
	}
	prefix = strings.TrimPrefix(prefix, "@")

	rows, err := s.db.Query("SELECT raw FROM logs WHERE json_valid(raw) ORDER BY timestamp DESC LIMIT 1000")
	if err != nil {
		return nil, fmt.Errorf("query json logs: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			fmt.Printf("warning: close json attribute rows: %v\n", err)
		}
	}()

	names := make(map[string]struct{})
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan json log: %w", err)
		}
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			continue
		}
		collectJSONAttributeNames(names, "", value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate json logs: %w", err)
	}

	result := make([]string, 0, len(names))
	for name := range names {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			continue
		}
		result = append(result, name)
	}
	sort.Strings(result)
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) DistinctJSONValues(field, prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 10
	}
	expr, args := attributeTextExpression(field)
	args = append(args, suggestionLikePrefix(prefix), limit)
	rows, err := s.db.Query(
		"SELECT DISTINCT value FROM (SELECT CAST("+expr+" AS TEXT) AS value FROM logs) WHERE value IS NOT NULL AND value <> '' AND value LIKE ? ESCAPE '\\' ORDER BY value LIMIT ?",
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("distinct json values for %s: %w", field, err)
	}
	return scanStringRows(rows)
}

func suggestionTokenStart(query string, cursor int) int {
	for i := cursor; i > 0; i-- {
		switch query[i-1] {
		case ' ', '\t', '\n', '\r', '(':
			return i
		}
	}
	return 0
}

func suggestionTokenEnd(query string, cursor int) int {
	for i := cursor; i < len(query); i++ {
		switch query[i] {
		case ' ', '\t', '\n', '\r', ')':
			return i
		}
	}
	return len(query)
}

func groupedFieldContext(prefix string) (string, bool) {
	colonParen := strings.LastIndex(prefix, ":(")
	if colonParen < 0 {
		return "", false
	}
	if strings.LastIndex(prefix[colonParen+2:], ")") >= 0 {
		return "", false
	}

	start := colonParen
	for start > 0 {
		switch prefix[start-1] {
		case ' ', '\t', '\n', '\r', '(':
			field := strings.TrimPrefix(prefix[start:colonParen], "@")
			return field, field != ""
		default:
			start--
		}
	}
	field := strings.TrimPrefix(prefix[start:colonParen], "@")
	return field, field != ""
}

func suggestionColumn(field string) (string, error) {
	field = normalizeQueryField(field)
	column, _, ok := reservedQueryColumn(field)
	if !ok {
		return "", fmt.Errorf("unsupported suggestion field %q", field)
	}
	return column, nil
}

func suggestionLikePrefix(prefix string) string {
	var b strings.Builder
	for _, r := range prefix {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String() + "%"
}

func scanStringRows(rows *sql.Rows) (values []string, err error) {
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close string rows: %w", closeErr)
		}
	}()

	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan string value: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func collectJSONAttributeNames(names map[string]struct{}, prefix string, value any) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	for key, child := range object {
		name := key
		if prefix != "" {
			name = prefix + "." + key
		}
		names[name] = struct{}{}
		collectJSONAttributeNames(names, name, child)
	}
}
