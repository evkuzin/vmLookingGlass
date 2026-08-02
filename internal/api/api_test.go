package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/model"
	"github.com/evkuzin/vmLookingGlass/internal/poll"
	"github.com/evkuzin/vmLookingGlass/internal/store"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var in = model.NewInterner()

func target(agent, pool, url string, labels map[string]string) model.Target {
	return model.Target{
		Agent:              agent,
		ScrapePool:         pool,
		ScrapeURL:          url,
		Labels:             in.Labels(labels),
		DiscoveredLabels:   in.Labels(map[string]string{"__address__": url, "job": pool}),
		Health:             model.HealthUp,
		LastScrape:         time.Now(),
		LastScrapeDuration: 0.25,
		LastSamplesScraped: 100,
	}
}

func result(name string, active ...model.Target) poll.AgentResult {
	return poll.AgentResult{
		Result: &poll.Result{Active: active},
		Status: model.AgentStatus{
			Name: name, URL: "http://" + name + ":8429",
			LastPoll: time.Now(), LastSuccess: time.Now(),
			Interval: time.Minute, ActiveTargets: len(active), UpTargets: len(active),
		},
	}
}

// testServer builds a handler over a store seeded with two agents.
func testServer(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	s := store.New()
	s.Apply(result("agent01",
		target("agent01", "pool-a", "http://h1:9100/metrics", map[string]string{"instance": "h1:9100", "job": "node"}),
		target("agent01", "pool-b", "http://h2:9100/metrics", map[string]string{"instance": "h2:9100", "job": "redis"}),
	))
	s.Apply(result("agent02",
		target("agent02", "pool-a", "http://h3:9100/metrics", map[string]string{"instance": "h3:9100", "job": "node"}),
	))
	return s, New(s, discardLogger()).Handler()
}

func get(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))

	var body map[string]any
	if ct := rec.Header().Get("Content-Type"); ct == "application/json" {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %s: decode: %v\nbody: %s", path, err, rec.Body.String())
		}
	}
	return rec, body
}

func TestTargetsPrometheusShape(t *testing.T) {
	_, h := testServer(t)
	rec, body := get(t, h, "/api/v1/targets")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body["status"] != "success" {
		t.Errorf("status = %v, want success", body["status"])
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data missing or wrong type: %T", body["data"])
	}
	active, ok := data["activeTargets"].([]any)
	if !ok {
		t.Fatalf("activeTargets missing: %T", data["activeTargets"])
	}
	if len(active) != 3 {
		t.Errorf("activeTargets = %d, want 3", len(active))
	}
	if _, ok := data["droppedTargets"]; !ok {
		t.Error("droppedTargets key absent; Prometheus clients expect it")
	}

	first := active[0].(map[string]any)
	for _, key := range []string{"discoveredLabels", "labels", "scrapePool", "scrapeUrl", "health", "lastError"} {
		if _, ok := first[key]; !ok {
			t.Errorf("target missing Prometheus field %q", key)
		}
	}
	// The added field is what makes the aggregated view useful.
	if _, ok := first["agent"]; !ok {
		t.Error("target missing the agent field")
	}
}

// A target reported by several agents appears once, listing all of them.
func TestTargetsMergedAcrossAgents(t *testing.T) {
	s := store.New()
	labels := map[string]string{"instance": "shared:9100", "job": "node"}
	const url = "http://shared:9100/metrics"
	s.Apply(result("agent01", target("agent01", "pool-a", url, labels)))
	s.Apply(result("agent02", target("agent02", "pool-a", url, labels)))

	h := New(s, discardLogger()).Handler()
	_, body := get(t, h, "/api/v1/targets")
	active := body["data"].(map[string]any)["activeTargets"].([]any)

	if len(active) != 1 {
		t.Fatalf("activeTargets = %d, want 1 merged row", len(active))
	}
	tgt := active[0].(map[string]any)
	agents, ok := tgt["agents"].([]any)
	if !ok || len(agents) != 2 {
		t.Errorf("agents = %v, want both", tgt["agents"])
	}
	if tgt["multiScraped"] != true {
		t.Error("merged target not flagged as multiScraped")
	}
}

func TestTargetsStateFilter(t *testing.T) {
	s := store.New()
	dropped := target("agent01", "pool-a", "http://h9:9100/metrics", map[string]string{"instance": "h9"})
	dropped.Dropped = true
	res := result("agent01", target("agent01", "pool-a", "http://h1:9100/metrics", map[string]string{"instance": "h1"}))
	res.Result.Dropped = []model.Target{dropped}
	s.Apply(res)

	h := New(s, discardLogger()).Handler()

	tests := []struct {
		state                   string
		wantActive, wantDropped int
	}{
		{"", 1, 1},
		{"any", 1, 1},
		{"active", 1, 0},
		{"dropped", 0, 1},
	}
	for _, tt := range tests {
		path := "/api/v1/targets"
		if tt.state != "" {
			path += "?state=" + tt.state
		}
		_, body := get(t, h, path)
		data := body["data"].(map[string]any)
		gotA := len(data["activeTargets"].([]any))
		gotD := len(data["droppedTargets"].([]any))
		if gotA != tt.wantActive || gotD != tt.wantDropped {
			t.Errorf("state=%q: got %d active, %d dropped, want %d/%d",
				tt.state, gotA, gotD, tt.wantActive, tt.wantDropped)
		}
	}
}

func TestTargetsScrapePoolFilter(t *testing.T) {
	_, h := testServer(t)
	_, body := get(t, h, "/api/v1/targets?scrapePool=pool-a")
	active := body["data"].(map[string]any)["activeTargets"].([]any)
	if len(active) != 2 {
		t.Errorf("activeTargets = %d, want 2 in pool-a", len(active))
	}
	for _, a := range active {
		if got := a.(map[string]any)["scrapePool"]; got != "pool-a" {
			t.Errorf("target from pool %v leaked into the filtered result", got)
		}
	}
}

func TestDroppedTargetCounts(t *testing.T) {
	s := store.New()
	d1 := target("agent01", "pool-a", "http://h8/metrics", map[string]string{"instance": "h8"})
	d1.Dropped = true
	d2 := target("agent01", "pool-b", "http://h9/metrics", map[string]string{"instance": "h9"})
	d2.Dropped = true
	res := result("agent01")
	res.Result.Dropped = []model.Target{d1, d2}
	s.Apply(res)

	h := New(s, discardLogger()).Handler()
	_, body := get(t, h, "/api/v1/targets")
	counts, ok := body["data"].(map[string]any)["droppedTargetCounts"].(map[string]any)
	if !ok {
		t.Fatalf("droppedTargetCounts missing: %T", body["data"].(map[string]any)["droppedTargetCounts"])
	}
	if counts["pool-a"] != float64(1) || counts["pool-b"] != float64(1) {
		t.Errorf("counts = %v, want one per pool", counts)
	}
}

func TestScrapePools(t *testing.T) {
	_, h := testServer(t)
	_, body := get(t, h, "/api/v1/scrape_pools")

	pools := body["data"].(map[string]any)["scrapePools"].([]any)
	if len(pools) != 2 {
		t.Fatalf("pools = %v, want 2", pools)
	}
	if pools[0] != "pool-a" || pools[1] != "pool-b" {
		t.Errorf("pools = %v, want sorted [pool-a pool-b]", pools)
	}
}

func TestSearchFilters(t *testing.T) {
	_, h := testServer(t)
	_, body := get(t, h, "/api/v1/search?filter=labels.job%3Dnode")

	data := body["data"].(map[string]any)
	if got := data["total"]; got != float64(2) {
		t.Errorf("total = %v, want 2", got)
	}
	targets := data["targets"].([]any)
	if len(targets) != 2 {
		t.Errorf("targets = %d, want 2", len(targets))
	}
}

func TestSearchMultipleFilters(t *testing.T) {
	_, h := testServer(t)
	_, body := get(t, h, "/api/v1/search?filter=labels.job%3Dnode&filter=agent%3Dagent01")

	if got := body["data"].(map[string]any)["total"]; got != float64(1) {
		t.Errorf("total = %v, want 1 after ANDing both filters", got)
	}
}

func TestSearchPagination(t *testing.T) {
	s := store.New()
	var targets []model.Target
	for i := range 25 {
		url := fmt.Sprintf("http://h%02d:9100/metrics", i)
		targets = append(targets, target("agent01", "pool-a", url, map[string]string{"instance": url}))
	}
	s.Apply(result("agent01", targets...))
	h := New(s, discardLogger()).Handler()

	_, body := get(t, h, "/api/v1/search?limit=10")
	data := body["data"].(map[string]any)
	if got := data["total"]; got != float64(25) {
		t.Errorf("total = %v, want the full match count 25", got)
	}
	if got := len(data["targets"].([]any)); got != 10 {
		t.Errorf("returned %d targets, want 10", got)
	}
	if data["truncated"] != true {
		t.Error("truncated flag not set when results exceed the limit")
	}

	// Offset walks the same stable ordering.
	_, page2 := get(t, h, "/api/v1/search?limit=10&offset=10")
	first := data["targets"].([]any)[0].(map[string]any)["scrapeUrl"]
	second := page2["data"].(map[string]any)["targets"].([]any)[0].(map[string]any)["scrapeUrl"]
	if first == second {
		t.Error("offset did not advance the result window")
	}
}

func TestSearchDefaultLimit(t *testing.T) {
	s := store.New()
	var targets []model.Target
	for i := range defaultSearchLimit + 50 {
		url := fmt.Sprintf("http://h%04d:9100/metrics", i)
		targets = append(targets, target("agent01", "pool-a", url, map[string]string{"instance": url}))
	}
	s.Apply(result("agent01", targets...))
	h := New(s, discardLogger()).Handler()

	_, body := get(t, h, "/api/v1/search")
	data := body["data"].(map[string]any)
	if got := len(data["targets"].([]any)); got != defaultSearchLimit {
		t.Errorf("returned %d targets, want the default cap %d", got, defaultSearchLimit)
	}
	if data["truncated"] != true {
		t.Error("truncated flag not set at the default cap")
	}
}

func TestSearchBadFilter(t *testing.T) {
	_, h := testServer(t)
	rec, body := get(t, h, "/api/v1/search?filter=bogusField%3Dx")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if body["status"] != "error" {
		t.Errorf("status = %v, want error", body["status"])
	}
	if body["error"] == nil || body["error"] == "" {
		t.Error("error message missing")
	}
}

func TestSearchBadPagination(t *testing.T) {
	_, h := testServer(t)
	for _, q := range []string{"limit=-1", "limit=abc", "offset=-5", "offset=xyz"} {
		rec, _ := get(t, h, "/api/v1/search?"+q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}

func TestAgentsEndpoint(t *testing.T) {
	_, h := testServer(t)
	_, body := get(t, h, "/api/v1/agents")

	agents := body["data"].(map[string]any)["agents"].([]any)
	if len(agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(agents))
	}
	first := agents[0].(map[string]any)
	for _, key := range []string{"name", "url", "lastPoll", "activeTargets", "healthy", "stale"} {
		if _, ok := first[key]; !ok {
			t.Errorf("agent missing field %q", key)
		}
	}
	if first["name"] != "agent01" {
		t.Errorf("agents not sorted by name: first = %v", first["name"])
	}
}

func TestAgentsReportsFailure(t *testing.T) {
	s := store.New()
	s.Apply(poll.AgentResult{Status: model.AgentStatus{
		Name: "broken", URL: "http://broken:8429",
		LastPoll: time.Now(), Interval: time.Minute, Err: "connection refused",
	}})
	h := New(s, discardLogger()).Handler()

	_, body := get(t, h, "/api/v1/agents")
	agent := body["data"].(map[string]any)["agents"].([]any)[0].(map[string]any)
	if agent["healthy"] != false {
		t.Error("failing agent reported healthy")
	}
	if agent["stale"] != true {
		t.Error("agent that never succeeded not reported stale")
	}
	if agent["error"] != "connection refused" {
		t.Errorf("error = %v", agent["error"])
	}
}

func TestHealthEndpoints(t *testing.T) {
	_, h := testServer(t)
	for _, path := range []string{"/-/healthy", "/-/ready"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	_, h := testServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/targets", http.NoBody))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

func TestUnknownPath(t *testing.T) {
	_, h := testServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nonexistent", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
