package server

import (
	"fmt"
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

	context := store.SuggestionContext(query, cursor)
	suggestions, err := s.querySuggestions(context, 10)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, querySuggestionResponse{
		ReplaceStart: context.ReplaceStart,
		ReplaceEnd:   context.ReplaceEnd,
		Suggestions:  suggestions,
	})
}

func (s *Server) querySuggestions(context store.QuerySuggestionContext, limit int) ([]querySuggestion, error) {
	if limit <= 0 {
		limit = 10
	}
	if context.ValueMode {
		return s.queryValueSuggestions(context, limit)
	}
	return s.queryFieldSuggestions(context, limit)
}

func (s *Server) queryFieldSuggestions(context store.QuerySuggestionContext, limit int) ([]querySuggestion, error) {
	fields := []querySuggestion{
		fieldSuggestion("service", "Nomad service/job"),
		fieldSuggestion("status", "status category"),
		fieldSuggestion("stream", "stdout or stderr"),
		fieldSuggestion("task", "Nomad task"),
		fieldSuggestion("level", "raw log level"),
		fieldSuggestion("message", "log message"),
		fieldSuggestion("raw", "raw log payload"),
		fieldSuggestion("alloc_id", "allocation id"),
		{Kind: "field", Label: "*:", Detail: "full text", Replacement: "*:"},
	}

	prefix := strings.ToLower(context.Prefix)
	if context.Negated {
		for i := range fields {
			fields[i].Replacement = "-" + fields[i].Replacement
		}
	}

	result := filterQuerySuggestions(fields, prefix, limit)
	if strings.HasPrefix(context.Prefix, "@") && len(result) < limit {
		attributes, err := s.store.JSONAttributeNames(context.Prefix, limit-len(result))
		if err != nil {
			return result, err
		}
		for _, attribute := range attributes {
			replacement := "@" + attribute + ":"
			if context.Negated {
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

func (s *Server) queryValueSuggestions(context store.QuerySuggestionContext, limit int) ([]querySuggestion, error) {
	field := strings.ToLower(strings.TrimPrefix(context.Field, "@"))
	var values []string
	var err error
	detail := "value"

	switch field {
	case "service", "job":
		values, err = s.serviceSuggestionValues(context.Prefix, limit)
		detail = "service"
	case "status":
		values = filterStrings([]string{"emergency", "error", "warn", "notice", "info", "debug", "ok"}, context.Prefix, limit)
		detail = "status"
	case "stream":
		values = filterStrings([]string{"stderr", "stdout"}, context.Prefix, limit)
		detail = "stream"
	case "level":
		values, err = s.store.DistinctValues("level", context.Prefix, limit)
		if len(values) == 0 && err == nil {
			values = filterStrings([]string{"ERROR", "WARN", "INFO", "DEBUG", "TRACE", "UNKNOWN"}, context.Prefix, limit)
		}
		detail = "level"
	case "task":
		values, err = s.store.DistinctValues("task", context.Prefix, limit)
		detail = "task"
	case "alloc", "alloc_id", "allocation":
		values, err = s.store.DistinctValues("alloc_id", context.Prefix, limit)
		detail = "allocation"
	case "message", "raw", "content", "*":
		values = nil
	default:
		values, err = s.store.DistinctJSONValues(field, context.Prefix, limit)
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

func (s *Server) serviceSuggestionValues(prefix string, limit int) ([]string, error) {
	jobs, err := s.nomad.ListJobs()
	if err != nil {
		fmt.Printf("warning: load services for query suggestions: %v\n", err)
		return s.store.DistinctValues("job", prefix, limit)
	}

	visible := s.visibleJobs(jobs)
	values := make([]string, 0, len(visible))
	for _, job := range visible {
		values = append(values, job.ID)
	}
	return filterStrings(values, prefix, limit), nil
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
