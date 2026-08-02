// Package search filters aggregated scrape targets.
//
// The filter syntax matches the one used by the standalone targets_finder
// tool, so existing expressions keep working, with agent and state filters
// added for the aggregated view.
package search

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/evkuzin/vmLookingGlass/internal/model"
	"github.com/evkuzin/vmLookingGlass/internal/store"
)

// Target states accepted by the state filter.
const (
	StateActive  = "active"
	StateDropped = "dropped"
	StateAny     = "any"
)

// matcher tests one condition against a target. Label matchers need the
// interner because a label carries indices, not text.
type matcher interface {
	match(in *model.Interner, e *store.Entry) bool
}

// Query is a set of filters that a target must all satisfy.
type Query struct {
	exprs    []string
	matchers []matcher
}

// Parse compiles filter expressions into a Query. Blank expressions are
// ignored, so an empty query matches everything.
func Parse(exprs []string) (*Query, error) {
	q := &Query{}
	for _, expr := range exprs {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			continue
		}
		m, err := parseOne(expr)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", expr, err)
		}
		q.exprs = append(q.exprs, expr)
		q.matchers = append(q.matchers, m)
	}
	return q, nil
}

// Match reports whether a target satisfies every filter.
func (q *Query) Match(in *model.Interner, e *store.Entry) bool {
	for _, m := range q.matchers {
		if !m.match(in, e) {
			return false
		}
	}
	return true
}

// Filter returns the matching targets, preserving input order.
func (q *Query) Filter(in *model.Interner, targets []*store.Entry) []*store.Entry {
	if len(q.matchers) == 0 {
		return targets
	}
	out := make([]*store.Entry, 0, len(targets))
	for _, e := range targets {
		if q.Match(in, e) {
			out = append(out, e)
		}
	}
	return out
}

// Empty reports whether the query has no filters.
func (q *Query) Empty() bool { return len(q.matchers) == 0 }

// String renders the query as its original expressions.
func (q *Query) String() string { return strings.Join(q.exprs, " ") }

func parseOne(expr string) (matcher, error) {
	switch {
	case strings.HasPrefix(expr, "labels."):
		return parseLabel(expr)
	case strings.HasPrefix(expr, "scrapePool"):
		return parseRegexField(expr, "scrapePool", func(e *store.Entry) string {
			return e.ScrapePool
		})
	case strings.HasPrefix(expr, "agent"):
		return parseAgent(expr)
	case strings.HasPrefix(expr, "health"):
		return parseHealth(expr)
	case strings.HasPrefix(expr, "state"):
		return parseState(expr)
	case strings.HasPrefix(expr, "lastScrapeDuration"):
		return parseFloatCmp(expr)
	case strings.HasPrefix(expr, "lastSamplesScraped"):
		return parseIntCmp(expr)
	}
	return nil, fmt.Errorf("unknown filter")
}

// labelMatcher tests one label against a pattern.
type labelMatcher struct {
	name string
	re   *regexp.Regexp
	// negated inverts the result, and makes an absent label a match, since a
	// label that is not present cannot match the pattern.
	negated bool
}

func (m *labelMatcher) match(in *model.Interner, e *store.Entry) bool {
	if !e.Labels.Has(in, m.name) {
		return m.negated
	}
	got := m.re.MatchString(e.Labels.Get(in, m.name))
	return got != m.negated
}

// parseLabel handles labels.<name>=<regex>, =~ and !~.
//
// "=" anchors the pattern against the whole value while "=~" and "!~" search
// for it anywhere, matching the behavior of the original tool.
func parseLabel(expr string) (matcher, error) {
	rest := strings.TrimPrefix(expr, "labels.")

	var name, pattern string
	var negated, anchored bool
	switch {
	case strings.Contains(rest, "=~"):
		name, pattern, _ = strings.Cut(rest, "=~")
	case strings.Contains(rest, "!~"):
		name, pattern, _ = strings.Cut(rest, "!~")
		negated = true
	case strings.Contains(rest, "="):
		name, pattern, _ = strings.Cut(rest, "=")
		anchored = true
	default:
		return nil, fmt.Errorf("expected =, =~ or !~")
	}

	if name == "" {
		return nil, fmt.Errorf("empty label name")
	}
	re, err := compile(pattern, anchored)
	if err != nil {
		return nil, err
	}
	return &labelMatcher{name: name, re: re, negated: negated}, nil
}

// fieldMatcher tests a string field against an anchored pattern.
type fieldMatcher struct {
	re    *regexp.Regexp
	value func(*store.Entry) string
}

func (m *fieldMatcher) match(_ *model.Interner, e *store.Entry) bool {
	return m.re.MatchString(m.value(e))
}

func parseRegexField(expr, field string, value func(*store.Entry) string) (matcher, error) {
	pattern, err := valueAfter(expr, field)
	if err != nil {
		return nil, err
	}
	re, err := compile(pattern, true)
	if err != nil {
		return nil, err
	}
	return &fieldMatcher{re: re, value: value}, nil
}

// agentMatcher matches when any agent scraping the target matches, since a
// target merged across agents belongs to all of them.
type agentMatcher struct{ re *regexp.Regexp }

func (m *agentMatcher) match(_ *model.Interner, e *store.Entry) bool {
	return e.AnyAgent(m.re.MatchString)
}

func parseAgent(expr string) (matcher, error) {
	pattern, err := valueAfter(expr, "agent")
	if err != nil {
		return nil, err
	}
	re, err := compile(pattern, true)
	if err != nil {
		return nil, err
	}
	return &agentMatcher{re: re}, nil
}

// healthMatcher compares health exactly rather than as a pattern.
type healthMatcher struct{ want string }

func (m *healthMatcher) match(_ *model.Interner, e *store.Entry) bool { return e.Health == m.want }

func parseHealth(expr string) (matcher, error) {
	v, err := valueAfter(expr, "health")
	if err != nil {
		return nil, err
	}
	return &healthMatcher{want: v}, nil
}

// stateMatcher selects active or dropped targets.
type stateMatcher struct{ state string }

func (m *stateMatcher) match(_ *model.Interner, e *store.Entry) bool {
	switch m.state {
	case StateActive:
		return !e.Dropped
	case StateDropped:
		return e.Dropped
	default:
		return true
	}
}

func parseState(expr string) (matcher, error) {
	v, err := valueAfter(expr, "state")
	if err != nil {
		return nil, err
	}
	switch v {
	case StateActive, StateDropped, StateAny:
		return &stateMatcher{state: v}, nil
	}
	return nil, fmt.Errorf("state must be %s, %s or %s", StateActive, StateDropped, StateAny)
}

// Comparison operators for numeric filters, longest first so that >= is not
// read as > followed by a stray =.
var operators = []string{">=", "<=", "==", "!=", ">", "<"}

type floatMatcher struct {
	op    string
	want  float64
	value func(*store.Entry) float64
}

func (m *floatMatcher) match(_ *model.Interner, e *store.Entry) bool {
	return compareOrdered(m.op, m.value(e), m.want)
}

func parseFloatCmp(expr string) (matcher, error) {
	op, raw, err := splitOperator(expr, "lastScrapeDuration")
	if err != nil {
		return nil, err
	}
	want, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, fmt.Errorf("expected a number, got %q", raw)
	}
	return &floatMatcher{op: op, want: want, value: func(e *store.Entry) float64 {
		return e.LastScrapeDuration
	}}, nil
}

type intMatcher struct {
	op    string
	want  int64
	value func(*store.Entry) int64
}

func (m *intMatcher) match(_ *model.Interner, e *store.Entry) bool {
	return compareOrdered(m.op, m.value(e), m.want)
}

func parseIntCmp(expr string) (matcher, error) {
	op, raw, err := splitOperator(expr, "lastSamplesScraped")
	if err != nil {
		return nil, err
	}
	want, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("expected an integer, got %q", raw)
	}
	return &intMatcher{op: op, want: want, value: func(e *store.Entry) int64 {
		return e.LastSamplesScraped
	}}, nil
}

func compareOrdered[T int64 | float64](op string, got, want T) bool {
	switch op {
	case ">":
		return got > want
	case "<":
		return got < want
	case ">=":
		return got >= want
	case "<=":
		return got <= want
	case "!=":
		return got != want
	default:
		return got == want
	}
}

// splitOperator separates the comparison operator and value following field.
// A bare value means equality.
func splitOperator(expr, field string) (op, value string, err error) {
	rest := strings.TrimPrefix(expr, field)
	for _, o := range operators {
		if strings.HasPrefix(rest, o) {
			return o, strings.TrimSpace(strings.TrimPrefix(rest, o)), nil
		}
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", fmt.Errorf("expected a value")
	}
	return "==", rest, nil
}

// valueAfter returns the value following "field=".
func valueAfter(expr, field string) (string, error) {
	rest := strings.TrimPrefix(expr, field)
	v, ok := strings.CutPrefix(rest, "=")
	if !ok {
		return "", fmt.Errorf("expected %s=<value>", field)
	}
	if v == "" {
		return "", fmt.Errorf("expected a value after %s=", field)
	}
	return v, nil
}

// compile builds a pattern matcher. Anchored patterns must match the whole
// value; unanchored ones may match anywhere.
func compile(pattern string, anchored bool) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	if anchored {
		pattern = "^(?:" + pattern + ")$"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}
	return re, nil
}
