package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// searchStub serves a canned search response and records the query it saw.
type searchStub struct {
	*httptest.Server
	lastQuery url.Values
	targets   []map[string]any
	total     int
}

func newSearchStub(t *testing.T, targets []map[string]any, total int) *searchStub {
	t.Helper()
	s := &searchStub{targets: targets, total: total}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.lastQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"targets":   s.targets,
				"total":     s.total,
				"truncated": len(s.targets) < s.total,
			},
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func target(instance, health string, agents ...string) map[string]any {
	as := make([]any, len(agents))
	for i, a := range agents {
		as[i] = a
	}
	return map[string]any{
		"labels":             map[string]any{"instance": instance, "job": "node"},
		"discoveredLabels":   map[string]any{"__address__": instance},
		"scrapePool":         "pool-a",
		"scrapeUrl":          "http://" + instance + "/metrics",
		"health":             health,
		"lastError":          "",
		"lastScrapeDuration": 0.25,
		"lastSamplesScraped": float64(100),
		"agents":             as,
		"multiScraped":       len(agents) > 1,
	}
}

func run(t *testing.T, addr string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run(append([]string{"-addr", addr}, args...), &out)
	return out.String(), err
}

func TestFiltersArePassedThrough(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{target("h1:9100", "up", "a1")}, 1)

	if _, err := run(t, stub.URL, "labels.job=node", "health=up"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := stub.lastQuery["filter"]
	if len(got) != 2 || got[0] != "labels.job=node" || got[1] != "health=up" {
		t.Errorf("filter params = %v, want both filters forwarded", got)
	}
}

func TestLimitAndOffsetForwarded(t *testing.T) {
	stub := newSearchStub(t, nil, 0)

	if _, err := run(t, stub.URL, "-limit", "5", "-offset", "10"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := stub.lastQuery.Get("limit"); got != "5" {
		t.Errorf("limit = %q, want 5", got)
	}
	if got := stub.lastQuery.Get("offset"); got != "10" {
		t.Errorf("offset = %q, want 10", got)
	}
}

func TestTableOutput(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{
		target("h1:9100", "up", "agent01"),
		target("h2:9100", "down", "agent02"),
	}, 2)

	out, err := run(t, stub.URL)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"h1:9100", "h2:9100", "up", "down", "agent01", "agent02", "pool-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// The agent column is the reason this tool exists, so it must always be shown.
func TestTableShowsAllScrapingAgents(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{
		target("shared:9100", "up", "agent01", "agent02"),
	}, 1)

	out, err := run(t, stub.URL)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "agent01") || !strings.Contains(out, "agent02") {
		t.Errorf("output does not list both scraping agents:\n%s", out)
	}
}

func TestJSONOutputIsValid(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{target("h1:9100", "up", "a1")}, 1)

	out, err := run(t, stub.URL, "-o", "json")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var parsed any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
}

func TestTruncationIsReported(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{target("h1:9100", "up", "a1")}, 500)

	out, err := run(t, stub.URL)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Silently showing 1 of 500 would read as a complete result.
	if !strings.Contains(out, "500") {
		t.Errorf("output does not report the full match count:\n%s", out)
	}
}

func TestEmptyResult(t *testing.T) {
	stub := newSearchStub(t, nil, 0)

	out, err := run(t, stub.URL)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out == "" {
		t.Error("no output for an empty result; expected a message")
	}
}

func TestServerErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"status":"error","error":"bogus=x: unknown filter"}`)
	}))
	t.Cleanup(srv.Close)

	_, err := run(t, srv.URL, "bogus=x")
	if err == nil {
		t.Fatal("expected an error for a rejected filter")
	}
	if !strings.Contains(err.Error(), "unknown filter") {
		t.Errorf("error %q does not carry the server's message", err)
	}
}

func TestUnreachableServer(t *testing.T) {
	if _, err := run(t, "http://127.0.0.1:1"); err == nil {
		t.Error("expected an error for an unreachable server")
	}
}

// A bare host is accepted, so callers need not type the scheme.
func TestAddrShorthand(t *testing.T) {
	stub := newSearchStub(t, nil, 0)
	host := strings.TrimPrefix(stub.URL, "http://")

	if _, err := run(t, host); err != nil {
		t.Errorf("bare host address rejected: %v", err)
	}
}

func TestAgentsSubcommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents" {
			t.Errorf("path = %q, want /api/v1/agents", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"agents": []map[string]any{
					{"name": "agent01", "url": "http://a1:8429", "healthy": true, "stale": false,
						"activeTargets": 100, "upTargets": 98, "downTargets": 2, "droppedTargets": 5,
						"durationMs": float64(120)},
					{"name": "agent02", "url": "http://a2:8429", "healthy": false, "stale": true,
						"error": "connection refused", "activeTargets": 0, "durationMs": float64(0)},
				},
				"activeCount": 100, "upCount": 98, "downCount": 2, "droppedCount": 5,
			},
		})
	}))
	t.Cleanup(srv.Close)

	out, err := run(t, srv.URL, "-agents")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"agent01", "agent02", "connection refused"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestBadOutputFormat(t *testing.T) {
	stub := newSearchStub(t, nil, 0)
	if _, err := run(t, stub.URL, "-o", "yaml"); err == nil {
		t.Error("expected an error for an unsupported output format")
	}
}

func TestCSVOutput(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{
		target("h1:9100", "up", "agent01"),
		target("h2:9100", "down", "agent02", "agent03"),
	}, 2)

	out, err := run(t, stub.URL, "-o", "csv")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v\n%s", err, out)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want header plus two rows:\n%s", len(records), out)
	}
	if records[0][0] != "instance" {
		t.Errorf("first column = %q, want instance", records[0][0])
	}
	// A field holding several agents must stay one field, not split the row.
	for i, rec := range records {
		if len(rec) != len(records[0]) {
			t.Errorf("record %d has %d fields, header has %d: %q",
				i, len(rec), len(records[0]), rec)
		}
	}
	agentsCol := -1
	for i, name := range records[0] {
		if name == "agents" {
			agentsCol = i
		}
	}
	if got := records[2][agentsCol]; got != "agent02,agent03" {
		t.Errorf("agents field = %q, want both agents in one field", got)
	}
}

func TestColumnSelection(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{target("h1:9100", "up", "agent01")}, 1)

	out, err := run(t, stub.URL, "-columns", "instance,agents")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "h1:9100") || !strings.Contains(out, "agent01") {
		t.Errorf("selected columns missing:\n%s", out)
	}
	// Columns that were not asked for must be absent.
	if strings.Contains(out, "pool-a") {
		t.Errorf("unselected column present:\n%s", out)
	}
}

func TestColumnSelectionAppliesToCSV(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{target("h1:9100", "up", "agent01")}, 1)

	out, err := run(t, stub.URL, "-o", "csv", "-columns", "health,instance")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "health,instance" {
		t.Errorf("header = %q, want the requested order", lines[0])
	}
	if lines[1] != "up,h1:9100" {
		t.Errorf("row = %q, want fields in the requested order", lines[1])
	}
}

func TestUnknownColumn(t *testing.T) {
	stub := newSearchStub(t, nil, 0)
	_, err := run(t, stub.URL, "-columns", "instance,nosuchcolumn")
	if err == nil {
		t.Fatal("expected an error for an unknown column")
	}
	// The message should say what is available.
	if !strings.Contains(err.Error(), "nosuchcolumn") {
		t.Errorf("error %q does not name the bad column", err)
	}
}

func TestTemplateOutput(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{
		target("h1:9100", "up", "agent01"),
		target("h2:9100", "down", "agent02"),
	}, 2)

	out, err := run(t, stub.URL, "-template", "{{.Instance}} => {{.Health}}")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "h1:9100 => up") {
		t.Errorf("template not applied:\n%s", out)
	}
	if !strings.Contains(out, "h2:9100 => down") {
		t.Errorf("template not applied to every row:\n%s", out)
	}
}

func TestTemplateCanReachLabels(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{target("h1:9100", "up", "agent01")}, 1)

	out, err := run(t, stub.URL, "-template", `{{index .Labels "job"}}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "node") {
		t.Errorf("template could not reach labels:\n%s", out)
	}
}

func TestBadTemplate(t *testing.T) {
	stub := newSearchStub(t, nil, 0)
	if _, err := run(t, stub.URL, "-template", "{{.Nope"); err == nil {
		t.Error("expected an error for a malformed template")
	}
}

// A template naming a field that does not exist should fail rather than
// silently print nothing.
func TestTemplateUnknownField(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{target("h1:9100", "up", "a1")}, 1)
	if _, err := run(t, stub.URL, "-template", "{{.NoSuchField}}"); err == nil {
		t.Error("expected an error for an unknown template field")
	}
}

func TestNoHeaderFlag(t *testing.T) {
	stub := newSearchStub(t, []map[string]any{target("h1:9100", "up", "agent01")}, 1)

	out, err := run(t, stub.URL, "-o", "csv", "-no-header")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out, "instance,") {
		t.Errorf("header present despite -no-header:\n%s", out)
	}
	if !strings.Contains(out, "h1:9100") {
		t.Errorf("row missing:\n%s", out)
	}
}

// A dropped target has no post-relabel labels, so the instance column falls
// back to the discovered address rather than rendering blank.
func TestDroppedTargetShowsDiscoveredAddress(t *testing.T) {
	dropped := map[string]any{
		"labels":           map[string]any{},
		"discoveredLabels": map[string]any{"__address__": "h9:9119", "job": "pool-9"},
		"scrapePool":       "pool-9",
		"health":           "unknown",
		"agents":           []any{"agent01"},
	}
	stub := newSearchStub(t, []map[string]any{dropped}, 1)

	out, err := run(t, stub.URL)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "h9:9119") {
		t.Errorf("dropped target has no identifying column:\n%s", out)
	}
	if !strings.Contains(out, "pool-9") {
		t.Errorf("dropped target job not filled from discovered labels:\n%s", out)
	}
}

// Filters are accepted as repeated flags as well as positionally, so they can
// be placed anywhere on the command line.
func TestFilterFlag(t *testing.T) {
	stub := newSearchStub(t, nil, 0)

	if _, err := run(t, stub.URL, "-filter", "health=down", "-filter", "labels.job=node"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := stub.lastQuery["filter"]
	if len(got) != 2 || got[0] != "health=down" || got[1] != "labels.job=node" {
		t.Errorf("filter params = %v, want both flags forwarded", got)
	}
}

// Flag and positional filters combine rather than one replacing the other.
func TestFilterFlagAndPositional(t *testing.T) {
	stub := newSearchStub(t, nil, 0)

	if _, err := run(t, stub.URL, "-filter", "health=down", "labels.job=node"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := stub.lastQuery["filter"]
	if len(got) != 2 {
		t.Fatalf("filter params = %v, want both", got)
	}
}

// A target its agents disagree about must be visible as such, not shown with
// whichever health arrived last.
func TestDisputedTargetShown(t *testing.T) {
	tgt := target("h1:9100", "down", "agentA", "agentB")
	tgt["disputed"] = true
	tgt["healthByAgent"] = map[string]any{"agentA": "up", "agentB": "down"}
	stub := newSearchStub(t, []map[string]any{tgt}, 1)

	out, err := run(t, stub.URL, "-columns", "instance,health,disputed")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "true") {
		t.Errorf("disputed column not shown:\n%s", out)
	}
}

func TestDisputedInTemplate(t *testing.T) {
	tgt := target("h1:9100", "down", "agentA", "agentB")
	tgt["disputed"] = true
	tgt["healthByAgent"] = map[string]any{"agentA": "up", "agentB": "down"}
	stub := newSearchStub(t, []map[string]any{tgt}, 1)

	out, err := run(t, stub.URL, "-template", `{{.Instance}} {{index .HealthByAgent "agentA"}}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "up") {
		t.Errorf("per-agent health not reachable from a template:\n%s", out)
	}
}
