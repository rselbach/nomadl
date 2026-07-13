package store

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type queryTokenKind int

const (
	queryTokenEOF queryTokenKind = iota
	queryTokenWord
	queryTokenPhrase
	queryTokenAnd
	queryTokenOr
	queryTokenNot
	queryTokenMinus
	queryTokenColon
	queryTokenLParen
	queryTokenRParen
)

type queryToken struct {
	kind  queryTokenKind
	value string
}

type queryNodeKind int

const (
	queryNodeTerm queryNodeKind = iota
	queryNodeAnd
	queryNodeOr
	queryNodeNot
	queryNodeRange
	queryNodeCompare
)

type queryNode struct {
	kind     queryNodeKind
	field    string
	value    string
	quoted   bool
	children []*queryNode
	operator string
	lower    string
	upper    string
}

func ValidateQuery(query string) error {
	_, err := parseLogQuery(query)
	return err
}

func MatchQuery(entry LogEntry, query string) (bool, error) {
	node, err := parseLogQuery(query)
	if err != nil {
		return false, err
	}
	if node == nil {
		return true, nil
	}
	return node.match(entry), nil
}

func parseLogQuery(query string) (*queryNode, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	tokens, err := tokenizeQuery(query)
	if err != nil {
		return nil, err
	}
	parser := queryParser{tokens: tokens}
	node, err := parser.parseOr("")
	if err != nil {
		return nil, err
	}
	if parser.peek().kind != queryTokenEOF {
		return nil, fmt.Errorf("unexpected token %q", parser.peek().value)
	}
	return node, nil
}

func tokenizeQuery(input string) ([]queryToken, error) {
	tokens := []queryToken{}
	for i := 0; i < len(input); {
		if input[i] == ' ' || input[i] == '\t' || input[i] == '\n' || input[i] == '\r' {
			i++
			continue
		}

		switch input[i] {
		case '(':
			tokens = append(tokens, queryToken{kind: queryTokenLParen, value: "("})
			i++
			continue
		case ')':
			tokens = append(tokens, queryToken{kind: queryTokenRParen, value: ")"})
			i++
			continue
		case ':':
			tokens = append(tokens, queryToken{kind: queryTokenColon, value: ":"})
			i++
			continue
		case '-':
			tokens = append(tokens, queryToken{kind: queryTokenMinus, value: "-"})
			i++
			continue
		case '"':
			value, next, err := readQuotedToken(input, i+1)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, queryToken{kind: queryTokenPhrase, value: value})
			i = next
			continue
		}

		value, next := readWordToken(input, i)
		kind := queryTokenWord
		switch value {
		case "AND":
			kind = queryTokenAnd
		case "OR":
			kind = queryTokenOr
		case "NOT":
			kind = queryTokenNot
		}
		tokens = append(tokens, queryToken{kind: kind, value: value})
		i = next
	}

	tokens = append(tokens, queryToken{kind: queryTokenEOF})
	return tokens, nil
}

func readQuotedToken(input string, start int) (string, int, error) {
	var b strings.Builder
	for i := start; i < len(input); i++ {
		switch input[i] {
		case '\\':
			if i+1 >= len(input) {
				b.WriteByte(input[i])
				continue
			}
			i++
			b.WriteByte(input[i])
		case '"':
			return b.String(), i + 1, nil
		default:
			b.WriteByte(input[i])
		}
	}
	return "", len(input), fmt.Errorf("unterminated quoted string")
}

func readWordToken(input string, start int) (string, int) {
	var b strings.Builder
	for i := start; i < len(input); i++ {
		switch input[i] {
		case ' ', '\t', '\n', '\r', '(', ')', ':', '"':
			return b.String(), i
		case '\\':
			if i+1 >= len(input) {
				b.WriteByte(input[i])
				continue
			}
			i++
			b.WriteByte(input[i])
		default:
			b.WriteByte(input[i])
		}
	}
	return b.String(), len(input)
}

type queryParser struct {
	tokens []queryToken
	pos    int
}

func (p *queryParser) parseOr(fieldContext string) (*queryNode, error) {
	left, err := p.parseAnd(fieldContext)
	if err != nil {
		return nil, err
	}

	for p.match(queryTokenOr) {
		right, err := p.parseAnd(fieldContext)
		if err != nil {
			return nil, err
		}
		left = combineQueryNodes(queryNodeOr, left, right)
	}
	return left, nil
}

func (p *queryParser) parseAnd(fieldContext string) (*queryNode, error) {
	left, err := p.parseUnary(fieldContext)
	if err != nil {
		return nil, err
	}

	for {
		if p.match(queryTokenAnd) {
			right, err := p.parseUnary(fieldContext)
			if err != nil {
				return nil, err
			}
			left = combineQueryNodes(queryNodeAnd, left, right)
			continue
		}
		if !startsQueryTerm(p.peek().kind) {
			return left, nil
		}
		right, err := p.parseUnary(fieldContext)
		if err != nil {
			return nil, err
		}
		left = combineQueryNodes(queryNodeAnd, left, right)
	}
}

func (p *queryParser) parseUnary(fieldContext string) (*queryNode, error) {
	if p.match(queryTokenMinus) || p.match(queryTokenNot) {
		child, err := p.parseUnary(fieldContext)
		if err != nil {
			return nil, err
		}
		return &queryNode{kind: queryNodeNot, children: []*queryNode{child}}, nil
	}
	return p.parsePrimary(fieldContext)
}

func (p *queryParser) parsePrimary(fieldContext string) (*queryNode, error) {
	if p.match(queryTokenLParen) {
		node, err := p.parseOr(fieldContext)
		if err != nil {
			return nil, err
		}
		if !p.match(queryTokenRParen) {
			return nil, fmt.Errorf("missing closing parenthesis")
		}
		return node, nil
	}

	tok := p.next()
	if tok.kind != queryTokenWord && tok.kind != queryTokenPhrase {
		return nil, fmt.Errorf("expected search term, got %q", tok.value)
	}
	if p.match(queryTokenColon) {
		return p.parseFieldValue(tok.value)
	}
	return &queryNode{kind: queryNodeTerm, field: fieldContext, value: tok.value, quoted: tok.kind == queryTokenPhrase}, nil
}

func (p *queryParser) parseFieldValue(field string) (*queryNode, error) {
	if p.match(queryTokenLParen) {
		node, err := p.parseOr(field)
		if err != nil {
			return nil, err
		}
		if !p.match(queryTokenRParen) {
			return nil, fmt.Errorf("missing closing parenthesis after %s", field)
		}
		return node, nil
	}

	tok := p.next()
	if tok.kind != queryTokenWord && tok.kind != queryTokenPhrase {
		return nil, fmt.Errorf("expected value after %s", field)
	}
	if tok.kind == queryTokenWord && strings.HasPrefix(tok.value, "[") {
		return p.parseRange(field, tok.value)
	}
	if tok.kind == queryTokenWord {
		if operator, value, ok := comparisonValue(tok.value); ok {
			return &queryNode{kind: queryNodeCompare, field: field, operator: operator, value: value}, nil
		}
	}
	return &queryNode{kind: queryNodeTerm, field: field, value: tok.value, quoted: tok.kind == queryTokenPhrase}, nil
}

func (p *queryParser) parseRange(field, first string) (*queryNode, error) {
	parts := []string{first}
	for !strings.HasSuffix(parts[len(parts)-1], "]") {
		tok := p.next()
		if tok.kind != queryTokenWord && tok.kind != queryTokenPhrase {
			return nil, fmt.Errorf("invalid range for %s", field)
		}
		parts = append(parts, tok.value)
	}

	rangeValue := strings.TrimSpace(strings.Join(parts, " "))
	rangeValue = strings.TrimPrefix(rangeValue, "[")
	rangeValue = strings.TrimSuffix(rangeValue, "]")
	rangeParts := strings.Split(rangeValue, " TO ")
	if len(rangeParts) != 2 {
		return nil, fmt.Errorf("range for %s must use [lower TO upper]", field)
	}
	return &queryNode{
		kind:  queryNodeRange,
		field: field,
		lower: strings.TrimSpace(rangeParts[0]),
		upper: strings.TrimSpace(rangeParts[1]),
	}, nil
}

func (p *queryParser) peek() queryToken {
	if p.pos >= len(p.tokens) {
		return queryToken{kind: queryTokenEOF}
	}
	return p.tokens[p.pos]
}

func (p *queryParser) next() queryToken {
	tok := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return tok
}

func (p *queryParser) match(kind queryTokenKind) bool {
	if p.peek().kind != kind {
		return false
	}
	p.pos++
	return true
}

func startsQueryTerm(kind queryTokenKind) bool {
	switch kind {
	case queryTokenWord, queryTokenPhrase, queryTokenMinus, queryTokenNot, queryTokenLParen:
		return true
	default:
		return false
	}
}

func combineQueryNodes(kind queryNodeKind, left, right *queryNode) *queryNode {
	children := []*queryNode{}
	if left.kind == kind {
		children = append(children, left.children...)
	} else {
		children = append(children, left)
	}
	if right.kind == kind {
		children = append(children, right.children...)
	} else {
		children = append(children, right)
	}
	return &queryNode{kind: kind, children: children}
}

func comparisonValue(value string) (string, string, bool) {
	for _, operator := range []string{">=", "<=", ">", "<"} {
		if strings.HasPrefix(value, operator) && len(value) > len(operator) {
			return operator, strings.TrimSpace(value[len(operator):]), true
		}
	}
	return "", "", false
}

func (n *queryNode) sql() (string, []any, error) {
	switch n.kind {
	case queryNodeTerm:
		return termSQL(n.field, n.value, n.quoted)
	case queryNodeCompare:
		return comparisonSQL(n.field, n.operator, n.value)
	case queryNodeRange:
		return rangeSQL(n.field, n.lower, n.upper)
	case queryNodeNot:
		clause, args, err := n.children[0].sql()
		if err != nil {
			return "", nil, err
		}
		return "NOT (" + clause + ")", args, nil
	case queryNodeAnd, queryNodeOr:
		operator := " AND "
		if n.kind == queryNodeOr {
			operator = " OR "
		}
		clauses := make([]string, 0, len(n.children))
		args := []any{}
		for _, child := range n.children {
			clause, childArgs, err := child.sql()
			if err != nil {
				return "", nil, err
			}
			clauses = append(clauses, "("+clause+")")
			args = append(args, childArgs...)
		}
		return strings.Join(clauses, operator), args, nil
	default:
		return "", nil, fmt.Errorf("unknown query node")
	}
}

func termSQL(field, value string, quoted bool) (string, []any, error) {
	field = normalizeQueryField(field)
	if field == "" {
		return textLikeSQL("message"), []any{likePattern(value, quoted, true)}, nil
	}
	if field == "*" {
		return fullTextSQL(), fullTextArgs(value, quoted), nil
	}
	if field == "status" {
		return statusSQL(value, quoted)
	}
	if column, mode, ok := reservedQueryColumn(field); ok {
		if isExistenceTerm(value, quoted) {
			return column + " <> ''", nil, nil
		}
		contains := mode == queryFieldContains
		if shouldUseLike(value, quoted, contains) {
			return textLikeSQL(column), []any{likePattern(value, quoted, contains)}, nil
		}
		return column + " COLLATE NOCASE = ?", []any{value}, nil
	}
	return attributeTermSQL(field, value, quoted)
}

func fullTextSQL() string {
	columns := []string{"message", "raw", "job", "alloc_id", "task", "level", "stream"}
	clauses := make([]string, 0, len(columns))
	for _, column := range columns {
		clauses = append(clauses, textLikeSQL(column))
	}
	return strings.Join(clauses, " OR ")
}

func fullTextArgs(value string, quoted bool) []any {
	pattern := likePattern(value, quoted, true)
	args := make([]any, 0, 7)
	for range 7 {
		args = append(args, pattern)
	}
	return args
}

type queryFieldMode string

const (
	queryFieldExact    queryFieldMode = "exact"
	queryFieldContains queryFieldMode = "contains"
)

func reservedQueryColumn(field string) (string, queryFieldMode, bool) {
	switch field {
	case "service", "job":
		return "job", queryFieldExact, true
	case "level":
		return "level", queryFieldExact, true
	case "task":
		return "task", queryFieldExact, true
	case "stream":
		return "stream", queryFieldExact, true
	case "message", "content":
		return "message", queryFieldContains, true
	case "raw":
		return "raw", queryFieldContains, true
	case "alloc", "alloc_id", "allocation":
		return "alloc_id", queryFieldExact, true
	default:
		return "", "", false
	}
}

func normalizeQueryField(field string) string {
	field = strings.TrimSpace(field)
	if strings.HasPrefix(field, "@") || strings.HasPrefix(field, "#") {
		return field
	}
	return strings.ToLower(field)
}

func statusSQL(value string, quoted bool) (string, []any, error) {
	if isExistenceTerm(value, quoted) {
		return "level <> ''", nil, nil
	}
	if !quoted && !strings.ContainsAny(value, "*?") {
		if levels := levelsForStatus(value); len(levels) > 0 {
			placeholders := make([]string, 0, len(levels))
			args := make([]any, 0, len(levels))
			for _, level := range levels {
				placeholders = append(placeholders, "?")
				args = append(args, level)
			}
			return "UPPER(level) IN (" + strings.Join(placeholders, ",") + ")", args, nil
		}
	}
	if shouldUseLike(value, quoted, false) {
		return textLikeSQL("level"), []any{likePattern(value, quoted, false)}, nil
	}
	return "level COLLATE NOCASE = ?", []any{value}, nil
}

func levelsForStatus(status string) []string {
	switch strings.ToLower(status) {
	case "emergency":
		return []string{"EMERGENCY", "ALERT", "CRITICAL", "CRIT", "FATAL", "PANIC"}
	case "error":
		return []string{"ERROR", "ERR"}
	case "warn", "warning":
		return []string{"WARN", "WARNING"}
	case "notice":
		return []string{"NOTICE"}
	case "info":
		return []string{"INFO"}
	case "debug":
		return []string{"DEBUG", "TRACE"}
	case "ok", "unknown":
		return []string{"OK", "SUCCESS", "UNKNOWN"}
	default:
		return nil
	}
}

func attributeTermSQL(field, value string, quoted bool) (string, []any, error) {
	if isExistenceTerm(value, quoted) {
		clause, args := attributeExistsSQL(field)
		return clause, args, nil
	}
	expr, args := attributeTextExpression(field)
	if shouldUseLike(value, quoted, false) {
		return "CAST(" + expr + " AS TEXT) LIKE ? ESCAPE '\\'", append(args, likePattern(value, quoted, false)), nil
	}
	return "CAST(" + expr + " AS TEXT) COLLATE NOCASE = ?", append(args, value), nil
}

func attributeExistsSQL(field string) (string, []any) {
	directPath, nestedPath := jsonPathsForField(field)
	if directPath == nestedPath {
		return "(CASE WHEN json_valid(raw) THEN json_type(raw, ?) END) IS NOT NULL", []any{directPath}
	}
	return "((CASE WHEN json_valid(raw) THEN json_type(raw, ?) END) IS NOT NULL OR (CASE WHEN json_valid(raw) THEN json_type(raw, ?) END) IS NOT NULL)", []any{directPath, nestedPath}
}

func attributeTextExpression(field string) (string, []any) {
	directPath, nestedPath := jsonPathsForField(field)
	if directPath == nestedPath {
		return "CASE WHEN json_valid(raw) THEN json_extract(raw, ?) END", []any{directPath}
	}
	return "CASE WHEN json_valid(raw) THEN COALESCE(json_extract(raw, ?), json_extract(raw, ?)) END", []any{directPath, nestedPath}
}

func comparisonSQL(field, operator, value string) (string, []any, error) {
	if _, err := strconv.ParseFloat(value, 64); err != nil {
		return "", nil, fmt.Errorf("comparison value %q must be numeric", value)
	}
	expr, args, err := numericFieldExpression(field)
	if err != nil {
		return "", nil, err
	}
	return "CAST(" + expr + " AS REAL) " + operator + " ?", append(args, value), nil
}

func rangeSQL(field, lower, upper string) (string, []any, error) {
	clauses := []string{}
	args := []any{}
	if lower != "*" {
		if _, err := strconv.ParseFloat(lower, 64); err != nil {
			return "", nil, fmt.Errorf("range lower bound %q must be numeric or *", lower)
		}
		expr, exprArgs, err := numericFieldExpression(field)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, "CAST("+expr+" AS REAL) >= ?")
		args = append(args, exprArgs...)
		args = append(args, lower)
	}
	if upper != "*" {
		if _, err := strconv.ParseFloat(upper, 64); err != nil {
			return "", nil, fmt.Errorf("range upper bound %q must be numeric or *", upper)
		}
		expr, exprArgs, err := numericFieldExpression(field)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, "CAST("+expr+" AS REAL) <= ?")
		args = append(args, exprArgs...)
		args = append(args, upper)
	}
	if len(clauses) == 0 {
		clause, args := attributeExistsSQL(field)
		return clause, args, nil
	}
	return strings.Join(clauses, " AND "), args, nil
}

func numericFieldExpression(field string) (string, []any, error) {
	field = normalizeQueryField(field)
	if column, _, ok := reservedQueryColumn(field); ok {
		return column, nil, nil
	}
	expr, args := attributeTextExpression(field)
	return expr, args, nil
}

func textLikeSQL(column string) string {
	return column + " LIKE ? ESCAPE '\\'"
}

func shouldUseLike(value string, quoted, containsDefault bool) bool {
	return containsDefault || (!quoted && strings.ContainsAny(value, "*?"))
}

func isExistenceTerm(value string, quoted bool) bool {
	return !quoted && value == "*"
}

func likePattern(value string, quoted, containsDefault bool) string {
	var b strings.Builder
	hasWildcard := false
	for _, r := range value {
		switch r {
		case '*':
			if quoted {
				b.WriteString("\\*")
				continue
			}
			hasWildcard = true
			b.WriteByte('%')
		case '?':
			if quoted {
				b.WriteString("\\?")
				continue
			}
			hasWildcard = true
			b.WriteByte('_')
		case '%', '_', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	pattern := b.String()
	if containsDefault && !hasWildcard {
		return "%" + pattern + "%"
	}
	return pattern
}

func jsonPathsForField(field string) (string, string) {
	field = strings.TrimPrefix(strings.TrimPrefix(field, "@"), "#")
	return jsonPathForKey(field), jsonPathForPath(field)
}

func jsonPathForKey(key string) string {
	return "$" + jsonPathSegment(key)
}

func jsonPathForPath(path string) string {
	parts := strings.Split(path, ".")
	var b strings.Builder
	b.WriteByte('$')
	for _, part := range parts {
		if part == "" {
			continue
		}
		b.WriteString(jsonPathSegment(part))
	}
	return b.String()
}

func jsonPathSegment(segment string) string {
	segment = strings.ReplaceAll(segment, `\`, `\\`)
	segment = strings.ReplaceAll(segment, `"`, `\"`)
	return `."` + segment + `"`
}

func (n *queryNode) match(entry LogEntry) bool {
	switch n.kind {
	case queryNodeTerm:
		return matchTerm(entry, n.field, n.value, n.quoted)
	case queryNodeCompare:
		return matchComparison(entry, n.field, n.operator, n.value)
	case queryNodeRange:
		return matchRange(entry, n.field, n.lower, n.upper)
	case queryNodeNot:
		return !n.children[0].match(entry)
	case queryNodeAnd:
		for _, child := range n.children {
			if !child.match(entry) {
				return false
			}
		}
		return true
	case queryNodeOr:
		for _, child := range n.children {
			if child.match(entry) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func matchTerm(entry LogEntry, field, value string, quoted bool) bool {
	field = normalizeQueryField(field)
	switch field {
	case "":
		return matchText(entry.Message, value, quoted, true)
	case "*":
		return matchText(entry.Message, value, quoted, true) ||
			matchText(entry.Raw, value, quoted, true) ||
			matchText(entry.Job, value, quoted, true) ||
			matchText(entry.AllocID, value, quoted, true) ||
			matchText(entry.Task, value, quoted, true) ||
			matchText(entry.Level, value, quoted, true) ||
			matchText(entry.Stream, value, quoted, true)
	case "status":
		if isExistenceTerm(value, quoted) {
			return entry.Level != ""
		}
		if !quoted && !strings.ContainsAny(value, "*?") {
			if levels := levelsForStatus(value); len(levels) > 0 {
				for _, level := range levels {
					if strings.EqualFold(entry.Level, level) {
						return true
					}
				}
				return false
			}
		}
		return matchText(entry.Level, value, quoted, false)
	case "service", "job":
		return matchStructuredText(entry.Job, value, quoted)
	case "level":
		return matchStructuredText(entry.Level, value, quoted)
	case "task":
		return matchStructuredText(entry.Task, value, quoted)
	case "stream":
		return matchStructuredText(entry.Stream, value, quoted)
	case "message", "content":
		return matchText(entry.Message, value, quoted, true)
	case "raw":
		return matchText(entry.Raw, value, quoted, true)
	case "alloc", "alloc_id", "allocation":
		return matchStructuredText(entry.AllocID, value, quoted)
	default:
		attribute, ok := entryAttribute(entry, field)
		if isExistenceTerm(value, quoted) {
			return ok
		}
		return ok && matchStructuredText(attribute, value, quoted)
	}
}

func matchStructuredText(text, value string, quoted bool) bool {
	if isExistenceTerm(value, quoted) {
		return text != ""
	}
	if !quoted && strings.ContainsAny(value, "*?") {
		return wildcardMatchFold(value, text)
	}
	return strings.EqualFold(text, value)
}

func matchText(text, value string, quoted, containsDefault bool) bool {
	if isExistenceTerm(value, quoted) {
		return text != ""
	}
	if !quoted && strings.ContainsAny(value, "*?") {
		return wildcardMatchFold(value, text)
	}
	if containsDefault {
		return strings.Contains(strings.ToLower(text), strings.ToLower(value))
	}
	return strings.EqualFold(text, value)
}

func wildcardMatchFold(pattern, text string) bool {
	return wildcardMatch(strings.ToLower(pattern), strings.ToLower(text))
}

func wildcardMatch(pattern, text string) bool {
	pRunes := []rune(pattern)
	tRunes := []rune(text)
	dp := make([][]bool, len(pRunes)+1)
	for i := range dp {
		dp[i] = make([]bool, len(tRunes)+1)
	}
	dp[0][0] = true
	for i := 1; i <= len(pRunes); i++ {
		if pRunes[i-1] == '*' {
			dp[i][0] = dp[i-1][0]
		}
	}
	for i := 1; i <= len(pRunes); i++ {
		for j := 1; j <= len(tRunes); j++ {
			switch pRunes[i-1] {
			case '*':
				dp[i][j] = dp[i-1][j] || dp[i][j-1]
			case '?':
				dp[i][j] = dp[i-1][j-1]
			default:
				dp[i][j] = dp[i-1][j-1] && pRunes[i-1] == tRunes[j-1]
			}
		}
	}
	return dp[len(pRunes)][len(tRunes)]
}

func entryAttribute(entry LogEntry, field string) (string, bool) {
	var raw any
	if err := json.Unmarshal([]byte(entry.Raw), &raw); err != nil {
		return "", false
	}
	field = strings.TrimPrefix(strings.TrimPrefix(field, "@"), "#")
	if direct, ok := valueFromRawMap(raw, field); ok {
		return formatAttributeValue(direct), true
	}
	value, ok := valueFromPath(raw, strings.Split(field, "."))
	if !ok {
		return "", false
	}
	return formatAttributeValue(value), true
}

func valueFromRawMap(raw any, key string) (any, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	value, ok := m[key]
	return value, ok
}

func valueFromPath(raw any, path []string) (any, bool) {
	if len(path) == 0 {
		return raw, true
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	next, ok := m[path[0]]
	if !ok {
		return nil, false
	}
	return valueFromPath(next, path[1:])
}

func formatAttributeValue(value any) string {
	switch value := value.(type) {
	case nil:
		return "null"
	case string:
		return value
	case float64, bool:
		return fmt.Sprintf("%v", value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return string(encoded)
	}
}

func matchComparison(entry LogEntry, field, operator, value string) bool {
	got, ok := numericFieldValue(entry, field)
	if !ok {
		return false
	}
	want, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return false
	}
	switch operator {
	case ">":
		return got > want
	case ">=":
		return got >= want
	case "<":
		return got < want
	case "<=":
		return got <= want
	default:
		return false
	}
}

func matchRange(entry LogEntry, field, lower, upper string) bool {
	got, ok := numericFieldValue(entry, field)
	if !ok {
		return false
	}
	if lower != "*" {
		lowerValue, err := strconv.ParseFloat(lower, 64)
		if err != nil || got < lowerValue {
			return false
		}
	}
	if upper != "*" {
		upperValue, err := strconv.ParseFloat(upper, 64)
		if err != nil || got > upperValue {
			return false
		}
	}
	return true
}

func numericFieldValue(entry LogEntry, field string) (float64, bool) {
	field = normalizeQueryField(field)
	if column, _, ok := reservedQueryColumn(field); ok {
		got, err := strconv.ParseFloat(entryColumnValue(entry, column), 64)
		return got, err == nil
	}
	value, ok := entryAttribute(entry, field)
	if !ok {
		return 0, false
	}
	got, err := strconv.ParseFloat(value, 64)
	return got, err == nil
}

func entryColumnValue(entry LogEntry, column string) string {
	switch column {
	case "job":
		return entry.Job
	case "alloc_id":
		return entry.AllocID
	case "task":
		return entry.Task
	case "level":
		return entry.Level
	case "message":
		return entry.Message
	case "raw":
		return entry.Raw
	case "stream":
		return entry.Stream
	default:
		return ""
	}
}
