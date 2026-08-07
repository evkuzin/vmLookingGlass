package search

import (
	"strings"
	"testing"

	"github.com/evkuzin/vmLookingGlass/internal/model"
	"github.com/evkuzin/vmLookingGlass/internal/store"
)

var in = model.NewInterner()

func entry(agent, pool, url string, labels map[string]string) *store.Entry {
	return store.NewEntry(model.Target{
		Agent:      agent,
		ScrapePool: pool,
		ScrapeURL:  url,
		Labels:     in.Labels(labels),
		Health:     model.HealthUp,
	}, agent)
}

func mustParse(t *testing.T, exprs ...string) *Query {
	t.Helper()
	q, err := Parse(exprs)
	if err != nil {
		t.Fatalf("Parse(%v): %v", exprs, err)
	}
	return q
}

func TestLabelExactMatch(t *testing.T) {
	e := entry("a1", "p", "http://h/m", map[string]string{"job": "node", "env": "prod"})

	tests := []struct {
		expr string
		want bool
	}{
		{"labels.job=node", true},
		{"labels.job=nod", false},   // anchored: not a prefix match
		{"labels.job=node.*", true}, // regex is allowed
		{"labels.job=vm", false},
		{"labels.missing=node", false}, // absent label does not match
	}
	for _, tt := range tests {
		q := mustParse(t, tt.expr)
		if got := q.Match(in, e); got != tt.want {
			t.Errorf("%s = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

// Exact match is anchored; regex match is a substring search.
func TestLabelRegexMatch(t *testing.T) {
	e := entry("a1", "p", "http://h/m", map[string]string{"job": "node_exporter"})

	tests := []struct {
		expr string
		want bool
	}{
		{"labels.job=~node", true},
		{"labels.job=~exporter", true},
		{"labels.job=~^node", true},
		{"labels.job=~^exporter", false},
		{"labels.job=node", false}, // anchored, so a substring fails
		{"labels.missing=~node", false},
	}
	for _, tt := range tests {
		q := mustParse(t, tt.expr)
		if got := q.Match(in, e); got != tt.want {
			t.Errorf("%s = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

// A negated regex holds for targets that lack the label entirely, since they
// cannot match the pattern.
func TestLabelNegatedRegexMatch(t *testing.T) {
	e := entry("a1", "p", "http://h/m", map[string]string{"job": "node_exporter"})

	tests := []struct {
		expr string
		want bool
	}{
		{"labels.job!~redis", true},
		{"labels.job!~node", false},
		{"labels.missing!~anything", true},
	}
	for _, tt := range tests {
		q := mustParse(t, tt.expr)
		if got := q.Match(in, e); got != tt.want {
			t.Errorf("%s = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

// != is the negation of =, so it is anchored where !~ is a substring search.
func TestLabelNegatedExactMatch(t *testing.T) {
	e := entry("a1", "p", "http://h/m", map[string]string{"job": "node_exporter"})

	tests := []struct {
		expr string
		want bool
	}{
		{"labels.job!=redis", true},
		{"labels.job!=node_exporter", false},
		{"labels.job!=node", true},         // anchored, so a substring still differs
		{"labels.job!~node", false},        // whereas !~ finds it anywhere
		{"labels.job!=node|redis", true},   // neither alternative is the whole value
		{"labels.missing!=anything", true}, // an absent label cannot equal it
	}
	for _, tt := range tests {
		q := mustParse(t, tt.expr)
		if got := q.Match(in, e); got != tt.want {
			t.Errorf("%s = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestScrapePoolFilter(t *testing.T) {
	e := entry("a1", "consul-services", "http://h/m", nil)

	tests := []struct {
		expr string
		want bool
	}{
		{"scrapePool=consul-services", true},
		{"scrapePool=consul.*", true},
		{"scrapePool=consul", false}, // anchored
		{"scrapePool=other", false},
	}
	for _, tt := range tests {
		q := mustParse(t, tt.expr)
		if got := q.Match(in, e); got != tt.want {
			t.Errorf("%s = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

// health is compared exactly, so regex metacharacters are not special.
func TestHealthFilter(t *testing.T) {
	up := entry("a1", "p", "http://h/m", nil)
	down := entry("a1", "p", "http://h2/m", nil)
	down.Health = model.HealthDown

	if !mustParse(t, "health=up").Match(in, up) {
		t.Error("health=up did not match an up target")
	}
	if mustParse(t, "health=up").Match(in, down) {
		t.Error("health=up matched a down target")
	}
	if !mustParse(t, "health=down").Match(in, down) {
		t.Error("health=down did not match a down target")
	}
	if mustParse(t, "health=u.").Match(in, up) {
		t.Error("health is compared exactly, so u. must not match up")
	}
	// Exact comparison, so a value that merely contains the filter must not
	// match. vmagent reports "unknown" for targets it has not scraped yet.
	unknown := entry("a1", "p", "http://h3/m", nil)
	unknown.Health = model.HealthUnknown
	if mustParse(t, "health=n").Match(in, unknown) {
		t.Error("health=n matched unknown; comparison must be exact, not substring")
	}
	if !mustParse(t, "health=unknown").Match(in, unknown) {
		t.Error("health=unknown did not match an unknown target")
	}
}

func TestNumericFilters(t *testing.T) {
	e := entry("a1", "p", "http://h/m", nil)
	e.LastScrapeDuration = 0.4
	e.LastSamplesScraped = 500

	tests := []struct {
		expr string
		want bool
	}{
		{"lastScrapeDuration>0.3", true},
		{"lastScrapeDuration>0.5", false},
		{"lastScrapeDuration<0.5", true},
		{"lastScrapeDuration>=0.4", true},
		{"lastScrapeDuration<=0.4", true},
		{"lastScrapeDuration==0.4", true},
		{"lastScrapeDuration!=0.4", false},
		{"lastScrapeDuration0.4", true}, // no operator means equality
		{"lastSamplesScraped>100", true},
		{"lastSamplesScraped>1000", false},
		{"lastSamplesScraped==500", true},
		{"lastSamplesScraped!=500", false},
	}
	for _, tt := range tests {
		q := mustParse(t, tt.expr)
		if got := q.Match(in, e); got != tt.want {
			t.Errorf("%s = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestAgentFilter(t *testing.T) {
	e := entry("vmagent01", "p", "http://h/m", nil)

	tests := []struct {
		expr string
		want bool
	}{
		{"agent=vmagent01", true},
		{"agent=vmagent.*", true},
		{"agent=other", false},
	}
	for _, tt := range tests {
		q := mustParse(t, tt.expr)
		if got := q.Match(in, e); got != tt.want {
			t.Errorf("%s = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

// A target merged across agents matches if any of its agents matches.
func TestAgentFilterMatchesAnyScrapingAgent(t *testing.T) {
	e := entry("a1", "p", "http://h/m", nil)
	e.SetAgentHealth("a2", model.HealthUp)

	if !mustParse(t, "agent=a2").Match(in, e) {
		t.Error("agent filter did not match the second scraping agent")
	}
	if mustParse(t, "agent=a3").Match(in, e) {
		t.Error("agent filter matched an agent that does not scrape the target")
	}
}

func TestStateFilter(t *testing.T) {
	active := entry("a1", "p", "http://h1/m", nil)
	dropped := entry("a1", "p", "http://h2/m", nil)
	dropped.Dropped = true

	tests := []struct {
		expr         string
		wantA, wantD bool
	}{
		{"state=active", true, false},
		{"state=dropped", false, true},
		{"state=any", true, true},
	}
	for _, tt := range tests {
		q := mustParse(t, tt.expr)
		if got := q.Match(in, active); got != tt.wantA {
			t.Errorf("%s on active = %v, want %v", tt.expr, got, tt.wantA)
		}
		if got := q.Match(in, dropped); got != tt.wantD {
			t.Errorf("%s on dropped = %v, want %v", tt.expr, got, tt.wantD)
		}
	}
}

// Multiple filters are ANDed.
func TestMultipleFiltersAreAnded(t *testing.T) {
	e := entry("a1", "consul-services", "http://h/m", map[string]string{"job": "node", "env": "prod"})

	if !mustParse(t, "labels.job=node", "labels.env=prod").Match(in, e) {
		t.Error("target satisfying both filters did not match")
	}
	if mustParse(t, "labels.job=node", "labels.env=dev").Match(in, e) {
		t.Error("target failing the second filter matched")
	}
	if !mustParse(t, "labels.job=node", "scrapePool=consul.*", "health=up").Match(in, e) {
		t.Error("target satisfying three filters did not match")
	}
}

func TestEmptyQueryMatchesEverything(t *testing.T) {
	e := entry("a1", "p", "http://h/m", nil)
	if !mustParse(t).Match(in, e) {
		t.Error("empty query did not match")
	}
	if !mustParse(t, "").Match(in, e) {
		t.Error("blank filter did not match")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []string{
		"unknownField=x",
		"labels.job",           // no operator
		"labels.=value",        // empty label name
		"scrapePool",           // no value
		"health",               // no value
		"state=bogus",          // not a known state
		"labels.job=[",         // invalid regex
		"lastScrapeDuration>x", // non-numeric
		"lastSamplesScraped>y",
	}
	for _, expr := range tests {
		t.Run(expr, func(t *testing.T) {
			if _, err := Parse([]string{expr}); err == nil {
				t.Errorf("Parse(%q) succeeded, want an error", expr)
			}
		})
	}
}

func TestFilterSnapshot(t *testing.T) {
	s := store.New(nil)
	snap := &store.Snapshot{
		Targets: []*store.Entry{
			entry("a1", "p1", "http://h1/m", map[string]string{"job": "node"}),
			entry("a1", "p2", "http://h2/m", map[string]string{"job": "redis"}),
			entry("a2", "p1", "http://h3/m", map[string]string{"job": "node"}),
		},
	}
	_ = s

	got := mustParse(t, "labels.job=node").Filter(in, snap.Targets)
	if len(got) != 2 {
		t.Fatalf("matched %d targets, want 2", len(got))
	}
	for _, e := range got {
		if e.Labels.Get(in, "job") != "node" {
			t.Errorf("non-matching target in results: %v", e.Labels)
		}
	}
}

// Filtering preserves the snapshot's ordering.
func TestFilterPreservesOrder(t *testing.T) {
	targets := []*store.Entry{
		entry("a1", "p", "http://h1/m", map[string]string{"job": "node"}),
		entry("a1", "p", "http://h2/m", map[string]string{"job": "node"}),
		entry("a1", "p", "http://h3/m", map[string]string{"job": "node"}),
	}
	got := mustParse(t, "labels.job=node").Filter(in, targets)
	for i := range got {
		if got[i].ScrapeURL != targets[i].ScrapeURL {
			t.Errorf("result %d = %s, want %s", i, got[i].ScrapeURL, targets[i].ScrapeURL)
		}
	}
}

func TestQueryString(t *testing.T) {
	q := mustParse(t, "labels.job=node", "health=up")
	if got := q.String(); got != "labels.job=node health=up" {
		t.Errorf("String() = %q", got)
	}
}

// Omitting the labels. prefix is the likeliest mistake, since every other
// filter names its field bare. The error has to say what to write instead.
func TestUnknownFilterSuggestsLabelPrefix(t *testing.T) {
	for _, expr := range []string{"instance=host042", "job=node", "env=~prod"} {
		_, err := Parse([]string{expr})
		if err == nil {
			t.Fatalf("%q parsed, want an error", expr)
		}
		if !strings.Contains(err.Error(), "labels.") {
			t.Errorf("%q: error %q does not mention the labels. prefix", expr, err)
		}
	}
}

// An unknown field that is not a label should list what is available rather
// than only saying no.
func TestUnknownFilterListsFields(t *testing.T) {
	_, err := Parse([]string{"nosuchfield=x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"health", "agent", "scrapePool", "state"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
