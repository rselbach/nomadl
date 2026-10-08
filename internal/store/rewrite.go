package store

import (
	"fmt"
	"slices"
	"strings"
)

// SelectionMode says how a query restricts a facet field.
type SelectionMode string

const (
	// SelectAll means no clause restricts the field.
	SelectAll SelectionMode = "all"
	// SelectInclude keeps only Values; with no Values it keeps nothing.
	SelectInclude SelectionMode = "include"
	// SelectExclude keeps everything but Values.
	SelectExclude SelectionMode = "exclude"
	// SelectCustom means the field is restricted in a way a list of
	// values can't show, such as a wildcard or a clause that mixes
	// fields.
	SelectCustom SelectionMode = "custom"
)

// Selection is which values of a facet field a query keeps.
type Selection struct {
	Mode   SelectionMode
	Values []string
}

// FacetFields lists the fields the sidebar edits, mapped to the aliases
// the query language accepts for them.
var FacetFields = map[string][]string{
	"service": {"service", "job"},
	"level":   {"level", "status"},
}

// SelectionOf reports which values of a facet field query keeps, judging
// by its top-level clauses. Level values are reported as level buckets.
func SelectionOf(query, field string) (Selection, error) {
	aliases, ok := FacetFields[field]
	if !ok {
		return Selection{}, fmt.Errorf("unknown facet field %q", field)
	}
	root, err := parseLogQuery(query)
	if err != nil {
		return Selection{}, err
	}

	var includes [][]string
	var excludes []string
	none := false
	for _, clause := range conjuncts(root) {
		switch fieldUse(clause, aliases) {
		case fieldUnused:
			continue
		case fieldMixed:
			return Selection{Mode: SelectCustom}, nil
		}
		negated, values, ok := facetValues(clause)
		if !ok {
			return Selection{Mode: SelectCustom}, nil
		}
		values = normalizeFacetValues(field, values)
		switch {
		case negated && slices.Contains(values, "*"):
			none = true
		case negated:
			excludes = append(excludes, values...)
		case slices.Contains(values, "*"):
			// field:* only requires the field to be set.
		default:
			includes = append(includes, values)
		}
	}

	switch {
	case none:
		return Selection{Mode: SelectInclude}, nil
	case len(includes) > 0:
		kept := includes[0]
		for _, set := range includes[1:] {
			kept = slices.DeleteFunc(kept, func(v string) bool { return !slices.Contains(set, v) })
		}
		kept = slices.DeleteFunc(kept, func(v string) bool { return slices.Contains(excludes, v) })
		return Selection{Mode: SelectInclude, Values: kept}, nil
	case len(excludes) > 0:
		return Selection{Mode: SelectExclude, Values: excludes}, nil
	default:
		return Selection{Mode: SelectAll}, nil
	}
}

// SetSelection rewrites query so a facet field keeps exactly sel. Clauses
// that restrict only that field are replaced by one clause at the end;
// every other clause keeps its original text.
func SetSelection(query, field string, sel Selection) (string, error) {
	aliases, ok := FacetFields[field]
	if !ok {
		return "", fmt.Errorf("unknown facet field %q", field)
	}
	root, err := parseLogQuery(query)
	if err != nil {
		return "", err
	}

	var kept []*queryNode
	for _, clause := range conjuncts(root) {
		if fieldUse(clause, aliases) != fieldOnly {
			kept = append(kept, clause)
		}
	}

	var clause string
	switch sel.Mode {
	case SelectAll:
	case SelectInclude:
		clause = fieldClause(field, sel.Values)
		if len(sel.Values) == 0 {
			clause = "-" + field + ":*"
		}
	case SelectExclude:
		if len(sel.Values) > 0 {
			clause = "-" + fieldClause(field, sel.Values)
		}
	default:
		return "", fmt.Errorf("can't set a %q selection", sel.Mode)
	}
	return joinClauses(query, kept, clause), nil
}

// AddFilter narrows query to rows where field matches value, or doesn't
// when exclude is set, keeping the existing clauses as written.
func AddFilter(query, field, value string, exclude bool) (string, error) {
	if strings.TrimSpace(field) == "" || strings.ContainsAny(field, " \t\r\n():\"") {
		return "", fmt.Errorf("invalid field %q", field)
	}
	root, err := parseLogQuery(query)
	if err != nil {
		return "", err
	}
	clause := field + ":" + QuoteValue(value)
	if exclude {
		clause = "-" + clause
	}
	return joinClauses(query, conjuncts(root), clause), nil
}

// QuoteValue quotes a query value when it would otherwise parse as
// something else: an operator, a wildcard, a range, or several terms.
func QuoteValue(value string) string {
	switch {
	case value == "":
		return `""`
	case value == "AND" || value == "OR" || value == "NOT",
		strings.HasPrefix(value, "-"), strings.HasPrefix(value, "["),
		strings.HasPrefix(value, ">"), strings.HasPrefix(value, "<"),
		strings.ContainsAny(value, " \t\r\n()\"\\:*?"):
		value = strings.ReplaceAll(value, `\`, `\\`)
		value = strings.ReplaceAll(value, `"`, `\"`)
		return `"` + value + `"`
	default:
		return value
	}
}

// conjuncts splits a query into the clauses that must all match.
func conjuncts(root *queryNode) []*queryNode {
	switch {
	case root == nil:
		return nil
	case root.kind == queryNodeAnd && !root.grouped:
		return root.children
	default:
		return []*queryNode{root}
	}
}

type fieldUsage int

const (
	fieldUnused fieldUsage = iota
	fieldOnly
	fieldMixed
)

// fieldUse reports whether a clause tests any of the aliased fields, and
// whether it tests nothing else.
func fieldUse(n *queryNode, aliases []string) fieldUsage {
	var on, off bool
	var walk func(*queryNode)
	walk = func(n *queryNode) {
		if len(n.children) > 0 {
			for _, child := range n.children {
				walk(child)
			}
			return
		}
		if slices.Contains(aliases, normalizeQueryField(n.field)) {
			on = true
		} else {
			off = true
		}
	}
	walk(n)
	switch {
	case !on:
		return fieldUnused
	case off:
		return fieldMixed
	default:
		return fieldOnly
	}
}

// facetValues reads a single-field clause shaped like field:v,
// field:(a OR b), or their negations. ok is false for any other shape,
// such as wildcards, comparisons, or AND groups.
func facetValues(n *queryNode) (negated bool, values []string, ok bool) {
	if n.kind == queryNodeNot {
		negated = true
		n = n.children[0]
	}
	terms := []*queryNode{n}
	if n.kind == queryNodeOr {
		terms = n.children
	}
	for _, term := range terms {
		if term.kind != queryNodeTerm {
			return false, nil, false
		}
		literal := term.quoted || term.value == "*" || !strings.ContainsAny(term.value, "*?")
		if !literal {
			return false, nil, false
		}
		values = append(values, term.value)
	}
	return negated, values, true
}

func normalizeFacetValues(field string, values []string) []string {
	if field != "level" {
		return values
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		switch {
		case value == "*":
		case len(levelsForBucket(value)) > 0:
			value = strings.ToLower(value)
			if value == "warning" {
				value = "warn"
			}
		case isCatchAllLevel(value):
			value = "ok"
		default:
			value = LevelBucket(value)
		}
		if !slices.Contains(normalized, value) {
			normalized = append(normalized, value)
		}
	}
	return normalized
}

func fieldClause(field string, values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, QuoteValue(value))
	}
	if len(quoted) == 1 {
		return field + ":" + quoted[0]
	}
	return field + ":(" + strings.Join(quoted, " OR ") + ")"
}

// joinClauses rebuilds a query from kept clauses, copied verbatim from
// query, plus an extra clause. A clause containing a bare top-level OR is
// parenthesized when it gets company, since OR binds looser than the
// implicit AND between clauses.
func joinClauses(query string, kept []*queryNode, extra string) string {
	parts := make([]string, 0, len(kept)+1)
	for _, clause := range kept {
		parts = append(parts, query[clause.pos:clause.end])
	}
	if extra != "" {
		parts = append(parts, extra)
	}
	if len(parts) > 1 {
		for i, clause := range kept {
			if clause.kind == queryNodeOr && !clause.grouped {
				parts[i] = "(" + parts[i] + ")"
			}
		}
	}
	return strings.Join(parts, " ")
}
