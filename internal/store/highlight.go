package store

import (
	"slices"
	"strings"
)

// HighlightTerms returns the literal text a query's message terms look
// for, so the UI can mark matches. Terms under a negation are skipped,
// and wildcard terms contribute their literal fragments. An invalid query
// has no terms.
func HighlightTerms(query string) []string {
	root, err := parseLogQuery(query)
	if err != nil || root == nil {
		return nil
	}

	var terms []string
	add := func(term string) {
		if term != "" && !slices.Contains(terms, term) {
			terms = append(terms, term)
		}
	}
	var walk func(n *queryNode, negated bool)
	walk = func(n *queryNode, negated bool) {
		switch n.kind {
		case queryNodeNot:
			walk(n.children[0], !negated)
		case queryNodeAnd, queryNodeOr:
			for _, child := range n.children {
				walk(child, negated)
			}
		case queryNodeTerm:
			if negated {
				return
			}
			switch normalizeQueryField(n.field) {
			case "", "message", "content", "raw", "*":
			default:
				return
			}
			if n.quoted {
				add(n.value)
				return
			}
			for _, fragment := range strings.FieldsFunc(n.value, func(r rune) bool { return r == '*' || r == '?' }) {
				add(fragment)
			}
		}
	}
	walk(root, false)
	return terms
}
