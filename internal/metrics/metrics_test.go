package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/evkuzin/vmLookingGlass/internal/model"
	"github.com/evkuzin/vmLookingGlass/internal/netbox"
	"github.com/evkuzin/vmLookingGlass/internal/poll"
	"github.com/evkuzin/vmLookingGlass/internal/store"
)

// One interner for the package: labels are indices into whichever interner
// produced them, so the store and the collector have to share it.
var in = model.NewInterner()

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d", rec.Code)
	}
	return rec.Body.String()
}

// hasSample reports whether the exposition contains a sample line whose name
// and labels all match.
func hasSample(body, name string, labels ...string) bool {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		ok := true
		for _, l := range labels {
			if !strings.Contains(line, l) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func sampleValue(t *testing.T, body, name string, labels ...string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		match := true
		for _, l := range labels {
			if !strings.Contains(line, l) {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		fields := strings.Fields(line)
		return fields[len(fields)-1]
	}
	t.Fatalf("no sample %s%v in:\n%s", name, labels, body)
	return ""
}

func okResult(name string, active, up, down, dropped int) poll.AgentResult {
	return poll.AgentResult{
		Result: &poll.Result{},
		Status: model.AgentStatus{
			Name: name, URL: "http://" + name + ":8429",
			LastPoll: time.Now(), LastSuccess: time.Now(),
			Interval: time.Minute, Duration: 250 * time.Millisecond,
			ActiveTargets: active, UpTargets: up, DownTargets: down,
			DroppedTargets: dropped,
		},
	}
}

func failResult(name, err string) poll.AgentResult {
	return poll.AgentResult{
		Status: model.AgentStatus{
			Name: name, URL: "http://" + name + ":8429",
			LastPoll: time.Now(), Interval: time.Minute,
			Duration: time.Second, Err: err,
		},
	}
}

func TestPollCountersByOutcome(t *testing.T) {
	m := New(prometheus.NewRegistry(), store.New(in), in)

	m.RecordPoll(okResult("agent01", 5, 5, 0, 1))
	m.RecordPoll(okResult("agent01", 5, 5, 0, 1))
	m.RecordPoll(failResult("agent01", "connection refused"))

	body := scrape(t, m)
	if got := sampleValue(t, body, "vmlookingglass_agent_polls_total",
		`agent="agent01"`, `result="success"`); got != "2" {
		t.Errorf("success count = %s, want 2", got)
	}
	if got := sampleValue(t, body, "vmlookingglass_agent_polls_total",
		`agent="agent01"`, `result="error"`); got != "1" {
		t.Errorf("error count = %s, want 1", got)
	}
}

func TestNetboxRefreshMetrics(t *testing.T) {
	m := New(prometheus.NewRegistry(), store.New(in), in)

	body := scrape(t, m)
	if got := sampleValue(t, body, "vmlookingglass_netbox_devices"); got != "0" {
		t.Errorf("devices before the first refresh = %s, want 0", got)
	}
	if got := sampleValue(t, body, "vmlookingglass_netbox_last_success_timestamp_seconds"); got != "0" {
		t.Errorf("last success before the first refresh = %s, want 0", got)
	}

	m.RecordNetboxRefresh(netbox.Refresh{Outcome: netbox.OutcomeOK, Names: []string{"a", "b"}})
	m.RecordNetboxRefresh(netbox.Refresh{Outcome: netbox.OutcomeError})
	m.RecordNetboxRefresh(netbox.Refresh{Outcome: netbox.OutcomeRejected, Previous: 10, Next: 4})

	body = scrape(t, m)
	for _, result := range []string{"ok", "error", "rejected"} {
		if got := sampleValue(t, body, "vmlookingglass_netbox_refreshes_total",
			`result="`+result+`"`); got != "1" {
			t.Errorf("%s refreshes = %s, want 1", result, got)
		}
	}
	if got := sampleValue(t, body, "vmlookingglass_netbox_devices"); got != "2" {
		t.Errorf("devices = %s, want the last successful set of 2", got)
	}
	if got := sampleValue(t, body, "vmlookingglass_netbox_last_success_timestamp_seconds"); got == "0" {
		t.Error("last success not recorded after a successful refresh")
	}
}

func TestPollDurationRecorded(t *testing.T) {
	m := New(prometheus.NewRegistry(), store.New(in), in)
	m.RecordPoll(okResult("agent01", 1, 1, 0, 0))

	body := scrape(t, m)
	if !hasSample(body, "vmlookingglass_agent_poll_duration_seconds", `agent="agent01"`) {
		t.Errorf("poll duration not exported:\n%s", body)
	}
}

// Per-agent gauges let an operator alert on one agent going away, which a
// fleet-wide total would hide.
func TestPerAgentGauges(t *testing.T) {
	s := store.New(in)
	s.Apply(okResult("agent01", 5, 4, 1, 2))
	s.Apply(failResult("agent02", "boom"))
	s.Flush()
	m := New(prometheus.NewRegistry(), s, in)

	body := scrape(t, m)

	if got := sampleValue(t, body, "vmlookingglass_agent_up", `agent="agent01"`); got != "1" {
		t.Errorf("healthy agent up = %s, want 1", got)
	}
	if got := sampleValue(t, body, "vmlookingglass_agent_up", `agent="agent02"`); got != "0" {
		t.Errorf("failing agent up = %s, want 0", got)
	}
	if got := sampleValue(t, body, "vmlookingglass_agent_active_targets", `agent="agent01"`); got != "5" {
		t.Errorf("active targets = %s, want 5", got)
	}
	if got := sampleValue(t, body, "vmlookingglass_agent_dropped_targets", `agent="agent01"`); got != "2" {
		t.Errorf("dropped targets = %s, want 2", got)
	}
}

func TestPerAgentUpDownGauges(t *testing.T) {
	s := store.New(in)
	s.Apply(okResult("agent01", 10, 8, 2, 0))
	s.Flush()
	m := New(prometheus.NewRegistry(), s, in)

	body := scrape(t, m)
	if got := sampleValue(t, body, "vmlookingglass_agent_up_targets", `agent="agent01"`); got != "8" {
		t.Errorf("up targets = %s, want 8", got)
	}
	if got := sampleValue(t, body, "vmlookingglass_agent_down_targets", `agent="agent01"`); got != "2" {
		t.Errorf("down targets = %s, want 2", got)
	}
}

// The truncation flag comes from the poll, which compares the dropped count
// against the cap read from that agent's own flags.
func TestDroppedTruncatedFlag(t *testing.T) {
	s := store.New(in)
	s.Apply(okResult("normal", 5, 5, 0, 42))
	capped := okResult("capped", 5, 5, 0, 10000)
	capped.Status.MaxDroppedTargets = 10000
	capped.Status.DroppedTruncated = true
	s.Apply(capped)
	s.Flush()
	m := New(prometheus.NewRegistry(), s, in)

	body := scrape(t, m)
	if got := sampleValue(t, body, "vmlookingglass_agent_dropped_truncated", `agent="normal"`); got != "0" {
		t.Errorf("agent below the cap flagged truncated: %s", got)
	}
	if got := sampleValue(t, body, "vmlookingglass_agent_dropped_truncated", `agent="capped"`); got != "1" {
		t.Errorf("agent at the cap not flagged truncated: %s", got)
	}
}

func TestSnapshotGauges(t *testing.T) {
	in := model.NewInterner()
	s := store.New(in)

	tgt := func(agent, url string) model.Target {
		return model.Target{
			Agent: agent, ScrapePool: "p", ScrapeURL: url,
			Labels: in.Labels(map[string]string{"instance": url}),
			Health: model.HealthUp,
		}
	}
	r1 := okResult("agent01", 2, 2, 0, 0)
	r1.Result.Active = []model.Target{tgt("agent01", "http://h1/m"), tgt("agent01", "http://h2/m")}
	s.Apply(r1)
	r2 := okResult("agent02", 1, 1, 0, 0)
	r2.Result.Active = []model.Target{tgt("agent02", "http://h1/m")}
	s.Apply(r2)
	s.Flush()

	m := New(prometheus.NewRegistry(), s, in)
	body := scrape(t, m)

	// Two unique targets: h1 is reported by both agents.
	if got := sampleValue(t, body, "vmlookingglass_targets"); got != "2" {
		t.Errorf("targets = %s, want 2 unique", got)
	}
	if got := sampleValue(t, body, "vmlookingglass_multi_scraped_targets"); got != "1" {
		t.Errorf("multi-scraped = %s, want 1", got)
	}
	if got := sampleValue(t, body, "vmlookingglass_agents"); got != "2" {
		t.Errorf("agents = %s, want 2", got)
	}
}

// The interner is the reason fleet-wide aggregation fits in memory, so its
// size is worth watching.
func TestInternerGauge(t *testing.T) {
	in := model.NewInterner()
	in.Labels(map[string]string{"a": "1", "b": "2"})
	m := New(prometheus.NewRegistry(), store.New(in), in)

	if got := sampleValue(t, scrape(t, m), "vmlookingglass_interned_strings"); got != "4" {
		t.Errorf("interned strings = %s, want 4", got)
	}
}

// A removed agent must stop being exported, or dashboards keep showing an
// agent that is no longer configured.
func TestRemovedAgentGaugesDisappear(t *testing.T) {
	s := store.New(in)
	s.Apply(okResult("agent01", 5, 5, 0, 0))
	s.Apply(okResult("agent02", 3, 3, 0, 0))
	s.Flush()
	m := New(prometheus.NewRegistry(), s, in)

	if body := scrape(t, m); !hasSample(body, "vmlookingglass_agent_up", `agent="agent02"`) {
		t.Fatalf("agent02 missing before removal:\n%s", body)
	}

	s.RetainAgents([]string{"agent01"})
	s.Flush()

	body := scrape(t, m)
	if hasSample(body, "vmlookingglass_agent_up", `agent="agent02"`) {
		t.Errorf("removed agent still exported:\n%s", body)
	}
	if !hasSample(body, "vmlookingglass_agent_up", `agent="agent01"`) {
		t.Errorf("retained agent missing:\n%s", body)
	}
}

func TestStaleAgentReported(t *testing.T) {
	s := store.New(in)
	stale := okResult("agent01", 1, 1, 0, 0)
	stale.Status.LastSuccess = time.Now().Add(-10 * time.Minute)
	s.Apply(stale)
	s.Flush()
	m := New(prometheus.NewRegistry(), s, in)

	if got := sampleValue(t, scrape(t, m), "vmlookingglass_agent_stale", `agent="agent01"`); got != "1" {
		t.Errorf("stale = %s, want 1", got)
	}
}

func TestGoAndProcessCollectorsRegistered(t *testing.T) {
	m := New(prometheus.NewRegistry(), store.New(in), in)
	body := scrape(t, m)

	for _, want := range []string{"go_goroutines", "go_memstats_alloc_bytes"} {
		if !strings.Contains(body, want) {
			t.Errorf("runtime metric %q not exported", want)
		}
	}
}

func TestExpositionIsWellFormed(t *testing.T) {
	s := store.New(in)
	s.Apply(okResult("agent01", 5, 4, 1, 2))
	s.Flush()
	m := New(prometheus.NewRegistry(), s, in)
	m.RecordPoll(okResult("agent01", 5, 4, 1, 2))

	body := scrape(t, m)
	// Every metric needs HELP and TYPE, or promtool rejects the exposition.
	for _, name := range []string{
		"vmlookingglass_agent_polls_total",
		"vmlookingglass_agent_up",
		"vmlookingglass_targets",
		"vmlookingglass_interned_strings",
	} {
		if !strings.Contains(body, "# HELP "+name) {
			t.Errorf("%s has no HELP", name)
		}
		if !strings.Contains(body, "# TYPE "+name) {
			t.Errorf("%s has no TYPE", name)
		}
	}
}
