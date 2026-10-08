package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/rselbach/nomadl/internal/store"
)

type querySuggestionResponse struct {
	ReplaceStart int               `json:"replace_start"`
	ReplaceEnd   int               `json:"replace_end"`
	Suggestions  []querySuggestion `json:"suggestions"`
}

type querySuggestion struct {
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Detail      string `json:"detail"`
	Replacement string `json:"replacement"`
}

func (s *Server) handleQuerySuggestions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	cursor := len(query)
	if value := r.URL.Query().Get("cursor"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "cursor must be an integer"})
			return
		}
		cursor = parsed
	}

	qc := store.SuggestionContext(query, cursor)
	suggestions, err := s.querySuggestions(r.Context(), qc, 10)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, querySuggestionResponse{
		ReplaceStart: qc.ReplaceStart,
		ReplaceEnd:   qc.ReplaceEnd,
		Suggestions:  suggestions,
	})
}

func (s *Server) querySuggestions(ctx context.Context, qc store.QuerySuggestionContext, limit int) ([]querySuggestion, error) {
	if limit <= 0 {
		limit = 10
	}
	if qc.ValueMode {
		return s.queryValueSuggestions(ctx, qc, limit)
	}
	return s.queryFieldSuggestions(ctx, qc, limit)
}

func (s *Server) queryFieldSuggestions(ctx context.Context, qc store.QuerySuggestionContext, limit int) ([]querySuggestion, error) {
	fields := []querySuggestion{
		fieldSuggestion("service", "Nomad service/job"),
		fieldSuggestion("level", "log level"),
		fieldSuggestion("stream", "stdout or stderr"),
		fieldSuggestion("task", "Nomad task"),
		fieldSuggestion("message", "log message"),
		fieldSuggestion("raw", "raw log payload"),
		fieldSuggestion("alloc_id", "allocation id"),
		{Kind: "field", Label: "*:", Detail: "full text", Replacement: "*:"},
	}

	prefix := strings.ToLower(qc.Prefix)
	if qc.Negated {
		for i := range fields {
			fields[i].Replacement = "-" + fields[i].Replacement
		}
	}

	result := filterQuerySuggestions(fields, prefix, limit)
	if strings.HasPrefix(qc.Prefix, "@") && len(result) < limit {
		attributes, err := s.store.JSONAttributeNames(ctx, qc.Prefix, limit-len(result))
		if err != nil {
			return result, err
		}
		for _, attribute := range attributes {
			replacement := "@" + attribute + ":"
			if qc.Negated {
				replacement = "-" + replacement
			}
			result = append(result, querySuggestion{
				Kind:        "attribute",
				Label:       "@" + attribute + ":",
				Detail:      "JSON attribute",
				Replacement: replacement,
			})
		}
	}
	return result, nil
}

func (s *Server) queryValueSuggestions(ctx context.Context, qc store.QuerySuggestionContext, limit int) ([]querySuggestion, error) {
	field := strings.ToLower(strings.TrimPrefix(qc.Field, "@"))
	var values []string
	var err error
	detail := "value"

	switch field {
	case "service", "job":
		values, err = s.serviceSuggestionValues(ctx, qc.Prefix, limit)
		detail = "service"
	case "level", "status":
		values = filterStrings([]string{"emergency", "error", "warn", "notice", "info", "debug", "ok"}, qc.Prefix, limit)
		detail = "level"
	case "stream":
		values = filterStrings([]string{"stderr", "stdout"}, qc.Prefix, limit)
		detail = "stream"
	case "task":
		values, err = s.store.DistinctValues(ctx, "task", qc.Prefix, limit)
		detail = "task"
	case "alloc", "alloc_id", "allocation":
		values, err = s.store.DistinctValues(ctx, "alloc_id", qc.Prefix, limit)
		detail = "allocation"
	case "message", "raw", "content", "*":
		values = nil
	default:
		values, err = s.store.DistinctJSONValues(ctx, field, qc.Prefix, limit)
		detail = "JSON value"
	}
	if err != nil {
		return nil, err
	}

	suggestions := make([]querySuggestion, 0, len(values))
	for _, value := range values {
		suggestions = append(suggestions, querySuggestion{
			Kind:        "value",
			Label:       value,
			Detail:      detail,
			Replacement: quoteQueryValue(value),
		})
	}
	return suggestions, nil
}

func (s *Server) serviceSuggestionValues(ctx context.Context, prefix string, limit int) ([]string, error) {
	visible := s.ingest.visibleServices()
	if len(visible) == 0 {
		return s.store.DistinctValues(ctx, "job", prefix, limit)
	}
	return filterStrings(visible, prefix, limit), nil
}

func fieldSuggestion(field, detail string) querySuggestion {
	return querySuggestion{
		Kind:        "field",
		Label:       field + ":",
		Detail:      detail,
		Replacement: field + ":",
	}
}

func filterQuerySuggestions(suggestions []querySuggestion, prefix string, limit int) []querySuggestion {
	if limit <= 0 {
		return nil
	}
	result := make([]querySuggestion, 0, len(suggestions))
	for _, suggestion := range suggestions {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSuffix(suggestion.Label, ":")), prefix) {
			continue
		}
		result = append(result, suggestion)
		if len(result) == limit {
			return result
		}
	}
	return result
}

func filterStrings(values []string, prefix string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	prefix = strings.ToLower(prefix)
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		if prefix != "" && !strings.HasPrefix(strings.ToLower(value), prefix) {
			continue
		}
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) > limit {
		return result[:limit]
	}
	return result
}

func quoteQueryValue(value string) string {
	if value == "" {
		return value
	}
	if !strings.HasPrefix(value, "-") && !strings.ContainsAny(value, " \t\n\r()\"!:*?#") {
		return value
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}
