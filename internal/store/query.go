package store

import (
	"fmt"
	"sort"
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
	pos   int
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
	pos      int
	field    string
	value    string
	quoted   bool
	children []*queryNode
	operator string
	lower    string
	upper    string
}

// QueryError reports a query that doesn't parse or compile. Pos is the
// byte offset in the query where the problem starts.
type QueryError struct {
	Pos int
	Msg string
}

func (e *QueryError) Error() string {
	return e.Msg
}

func queryErrorf(pos int, format string, args ...any) *QueryError {
	return &QueryError{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

// ValidateQuery parses query and returns a *QueryError if it is invalid.
func ValidateQuery(query string) error {
	_, err := parseLogQuery(query)
	return err
}

func parseLogQuery(query string) (*queryNode, error) {
	if strings.TrimSpace(query) == "" {
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
	if tok := parser.peek(); tok.kind != queryTokenEOF {
		return nil, queryErrorf(tok.pos, "unexpected %q", tok.value)
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
			tokens = append(tokens, queryToken{kind: queryTokenLParen, value: "(", pos: i})
			i++
			continue
		case ')':
			tokens = append(tokens, queryToken{kind: queryTokenRParen, value: ")", pos: i})
			i++
			continue
		case ':':
			tokens = append(tokens, queryToken{kind: queryTokenColon, value: ":", pos: i})
			i++
			continue
		case '-':
			tokens = append(tokens, queryToken{kind: queryTokenMinus, value: "-", pos: i})
			i++
			continue
		case '"':
			value, next, ok := readQuotedToken(input, i+1)
			if !ok {
				return nil, queryErrorf(i, "unterminated quote")
			}
			tokens = append(tokens, queryToken{kind: queryTokenPhrase, value: value, pos: i})
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
		tokens = append(tokens, queryToken{kind: kind, value: value, pos: i})
		i = next
	}

	tokens = append(tokens, queryToken{kind: queryTokenEOF, pos: len(input)})
	return tokens, nil
}

func readQuotedToken(input string, start int) (string, int, bool) {
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
			return b.String(), i + 1, true
		default:
			b.WriteByte(input[i])
		}
	}
	return "", len(input), false
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
	if tok := p.peek(); tok.kind == queryTokenMinus || tok.kind == queryTokenNot {
		p.next()
		child, err := p.parseUnary(fieldContext)
		if err != nil {
			return nil, err
		}
		return &queryNode{kind: queryNodeNot, pos: tok.pos, children: []*queryNode{child}}, nil
	}
	return p.parsePrimary(fieldContext)
}

func (p *queryParser) parsePrimary(fieldContext string) (*queryNode, error) {
	if open := p.peek(); open.kind == queryTokenLParen {
		p.next()
		node, err := p.parseOr(fieldContext)
		if err != nil {
			return nil, err
		}
		if !p.match(queryTokenRParen) {
			return nil, queryErrorf(open.pos, "unclosed parenthesis")
		}
		return node, nil
	}

	tok := p.next()
	if tok.kind != queryTokenWord && tok.kind != queryTokenPhrase {
		if tok.kind == queryTokenEOF {
			return nil, queryErrorf(tok.pos, "query ends where a search term was expected")
		}
		return nil, queryErrorf(tok.pos, "expected a search term, got %q", tok.value)
	}
	if p.match(queryTokenColon) {
		return p.parseFieldValue(tok)
	}
	return &queryNode{kind: queryNodeTerm, pos: tok.pos, field: fieldContext, value: tok.value, quoted: tok.kind == queryTokenPhrase}, nil
}

func (p *queryParser) parseFieldValue(fieldTok queryToken) (*queryNode, error) {
	field := fieldTok.value
	if open := p.peek(); open.kind == queryTokenLParen {
		p.next()
		node, err := p.parseOr(field)
		if err != nil {
			return nil, err
		}
		if !p.match(queryTokenRParen) {
			return nil, queryErrorf(open.pos, "unclosed parenthesis after %s:", field)
		}
		return node, nil
	}

	tok := p.next()
	if tok.kind != queryTokenWord && tok.kind != queryTokenPhrase {
		return nil, queryErrorf(fieldTok.pos, "expected a value after %s:", field)
	}
	if tok.kind == queryTokenWord && strings.HasPrefix(tok.value, "[") {
		return p.parseRange(fieldTok, tok.value)
	}
	if tok.kind == queryTokenWord {
		if operator, value, ok := comparisonValue(tok.value); ok {
			return &queryNode{kind: queryNodeCompare, pos: fieldTok.pos, field: field, operator: operator, value: value}, nil
		}
	}
	return &queryNode{kind: queryNodeTerm, pos: fieldTok.pos, field: field, value: tok.value, quoted: tok.kind == queryTokenPhrase}, nil
}

func (p *queryParser) parseRange(fieldTok queryToken, first string) (*queryNode, error) {
	field := fieldTok.value
	parts := []string{first}
	for !strings.HasSuffix(parts[len(parts)-1], "]") {
		tok := p.next()
		if tok.kind != queryTokenWord && tok.kind != queryTokenPhrase {
			return nil, queryErrorf(fieldTok.pos, "unclosed range for %s:; use [lower TO upper]", field)
		}
		parts = append(parts, tok.value)
	}

	rangeValue := strings.TrimSpace(strings.Join(parts, " "))
	rangeValue = strings.TrimPrefix(rangeValue, "[")
	rangeValue = strings.TrimSuffix(rangeValue, "]")
	rangeParts := strings.Split(rangeValue, " TO ")
	if len(rangeParts) != 2 {
		return nil, queryErrorf(fieldTok.pos, "range for %s: must use [lower TO upper]", field)
	}
	return &queryNode{
		kind:  queryNodeRange,
		pos:   fieldTok.pos,
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
		return comparisonSQL(n.pos, n.field, n.operator, n.value)
	case queryNodeRange:
		return rangeSQL(n.pos, n.field, n.lower, n.upper)
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
	if field == "level" || field == "status" {
		return levelSQL(value, quoted)
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

// levelSQL matches a level bucket name such as "error" against every raw
// level in that bucket, and any other value against the raw level.
func levelSQL(value string, quoted bool) (string, []any, error) {
	if isExistenceTerm(value, quoted) {
		return "level <> ''", nil, nil
	}
	if !quoted && !strings.ContainsAny(value, "*?") {
		if isCatchAllLevel(value) {
			levels := bucketedLevels()
			placeholders := make([]string, 0, len(levels))
			args := make([]any, 0, len(levels))
			for _, level := range levels {
				placeholders = append(placeholders, "?")
				args = append(args, level)
			}
			return "UPPER(level) NOT IN (" + strings.Join(placeholders, ",") + ")", args, nil
		}
		if levels := levelsForBucket(value); len(levels) > 0 {
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

// levelBuckets groups raw log levels into the categories that level:
// matches and the sidebar shows. Levels not listed in any bucket belong
// to the "ok" catch-all so that unrecognized levels never silently
// disappear when level filters are applied.
var levelBuckets = map[string][]string{
	"emergency": {"EMERGENCY", "ALERT", "CRITICAL", "CRIT", "FATAL", "PANIC"},
	"error":     {"ERROR", "ERR"},
	"warn":      {"WARN", "WARNING"},
	"notice":    {"NOTICE"},
	"info":      {"INFO"},
	"debug":     {"DEBUG", "TRACE"},
}

func levelsForBucket(bucket string) []string {
	normalized := strings.ToLower(bucket)
	if normalized == "warning" {
		normalized = "warn"
	}
	return levelBuckets[normalized]
}

// errorLevels returns the levels counted as errors by the histogram:
// the error and emergency buckets.
func errorLevels() []string {
	levels := append([]string(nil), levelBuckets["error"]...)
	levels = append(levels, levelBuckets["emergency"]...)
	sort.Strings(levels)
	return levels
}

func isCatchAllLevel(level string) bool {
	switch strings.ToLower(level) {
	case "ok", "success", "unknown":
		return true
	default:
		return false
	}
}

// bucketedLevels returns the union of all non-catch-all bucket levels,
// sorted for deterministic SQL.
func bucketedLevels() []string {
	var all []string
	for _, levels := range levelBuckets {
		all = append(all, levels...)
	}
	sort.Strings(all)
	return all
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

func comparisonSQL(pos int, field, operator, value string) (string, []any, error) {
	if _, err := strconv.ParseFloat(value, 64); err != nil {
		return "", nil, queryErrorf(pos, "%s:%s needs a number, got %q", field, operator, value)
	}
	expr, args, err := numericFieldExpression(field)
	if err != nil {
		return "", nil, err
	}
	return "CAST(" + expr + " AS REAL) " + operator + " ?", append(args, value), nil
}

func rangeSQL(pos int, field, lower, upper string) (string, []any, error) {
	clauses := []string{}
	args := []any{}
	if lower != "*" {
		if _, err := strconv.ParseFloat(lower, 64); err != nil {
			return "", nil, queryErrorf(pos, "range lower bound %q must be a number or *", lower)
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
			return "", nil, queryErrorf(pos, "range upper bound %q must be a number or *", upper)
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

// likePattern turns a query value into a LIKE pattern. Unquoted * and ?
// become wildcards. Contains-mode fields match the pattern anywhere in
// the text, so "conn*" finds "opening connection"; exact-mode fields
// anchor it to the whole value.
func likePattern(value string, quoted, containsDefault bool) string {
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '*':
			if quoted {
				b.WriteString("\\*")
				continue
			}
			b.WriteByte('%')
		case '?':
			if quoted {
				b.WriteString("\\?")
				continue
			}
			b.WriteByte('_')
		case '%', '_', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	pattern := b.String()
	if containsDefault {
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
