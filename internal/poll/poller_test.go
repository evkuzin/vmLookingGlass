package poll

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/evkuzin/vmLookingGlass/internal/config"
	"github.com/evkuzin/vmLookingGlass/internal/model"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sameBacking reports whether two strings share a backing array, i.e. one is
// not a distinct copy of the other.
func sameBacking(a, b string) bool {
	return unsafe.StringData(a) == unsafe.StringData(b)
}

// targetsBody returns a minimal response with n active targets.
func targetsBody(n int) string {
	s := `{"status":"success","data":{"activeTargets":[`
	for i := range n {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf(`{"scrapePool":"p","scrapeUrl":"http://h%d/metrics",`+
			`"labels":{"instance":"h%d:9100"},"health":"up"}`, i, i)
	}
	return s + `],"droppedTargets":[]}}`
}

// agentServer is a stub vmagent.
type agentServer struct {
	*httptest.Server
	requests atomic.Int64

	mu    sync.Mutex
	paths []string
}

func newAgentServer(t *testing.T, handler http.HandlerFunc) *agentServer {
	t.Helper()
	as := &agentServer{}
	as.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		as.requests.Add(1)
		as.mu.Lock()
		as.paths = append(as.paths, r.URL.String())
		as.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(as.Close)
	return as
}

func okAgent(t *testing.T, n int) *agentServer {
	return newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, targetsBody(n))
	})
}

func agentCfg(name, url string) config.Agent {
	return config.Agent{
		Name: name, URL: url,
		Interval: time.Minute, Timeout: 5 * time.Second,
	}
}

// collector accumulates results delivered by a Runner.
type collector struct {
	mu   sync.Mutex
	got  []AgentResult
	ping chan struct{}
}

func newCollector() *collector {
	return &collector{ping: make(chan struct{}, 256)}
}

func (c *collector) publish(r AgentResult) {
	c.mu.Lock()
	c.got = append(c.got, r)
	c.mu.Unlock()
	select {
	case c.ping <- struct{}{}:
	default:
	}
}

func (c *collector) results() []AgentResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]AgentResult(nil), c.got...)
}

func (c *collector) countFor(name string) int {
	n := 0
	for _, r := range c.results() {
		if r.Status.Name == name {
			n++
		}
	}
	return n
}

// awaitResults waits until at least n results have been published.
func (c *collector) awaitResults(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for len(c.results()) < n {
		select {
		case <-c.ping:
		case <-deadline:
			t.Fatalf("timed out with %d of %d results", len(c.results()), n)
		}
	}
}

// runRunner starts a Runner in the background and stops it at test end.
func runRunner(t *testing.T, r *Runner, agents []config.Agent) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx, agents)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Runner did not stop after cancellation")
		}
	})
}

func TestPollAgentSuccess(t *testing.T) {
	srv := okAgent(t, 3)
	p := New(discardLogger())

	res, status := p.pollAgent(context.Background(), agentCfg("a1", srv.URL), model.NewInterner())
	if status.Err != "" {
		t.Fatalf("unexpected error: %s", status.Err)
	}
	if len(res.Active) != 3 {
		t.Errorf("active = %d, want 3", len(res.Active))
	}
	if status.ActiveTargets != 3 || status.UpTargets != 3 {
		t.Errorf("status counts = %d active / %d up, want 3/3",
			status.ActiveTargets, status.UpTargets)
	}
	if status.LastSuccess.IsZero() {
		t.Error("LastSuccess not set on a successful poll")
	}
	if status.Duration <= 0 {
		t.Error("Duration not recorded")
	}
}

// Dropped targets are needed for debugging relabel rules, so the poller must
// ask for them rather than only active ones.
func TestPollRequestsAllStates(t *testing.T) {
	srv := okAgent(t, 1)
	p := New(discardLogger())
	p.pollAgent(context.Background(), agentCfg("a1", srv.URL), model.NewInterner())

	srv.mu.Lock()
	defer srv.mu.Unlock()
	var got string
	for _, p := range srv.paths {
		if strings.HasPrefix(p, "/api/v1/targets") {
			if got != "" {
				t.Fatalf("targets requested more than once: %v", srv.paths)
			}
			got = p
		}
	}
	if got == "" {
		t.Fatalf("targets never requested: %v", srv.paths)
	}
	if !strings.Contains(got, "state=any") {
		t.Errorf("path = %q, want state=any so dropped targets are included", got)
	}
}

func TestPollAgentHTTPError(t *testing.T) {
	srv := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	p := New(discardLogger())

	res, status := p.pollAgent(context.Background(), agentCfg("a1", srv.URL), model.NewInterner())
	if status.Err == "" {
		t.Error("expected an error for HTTP 500")
	}
	if res != nil {
		t.Error("expected no result on failure")
	}
	if !status.LastSuccess.IsZero() {
		t.Error("LastSuccess set despite failure")
	}
}

// An agent can return an error status with a body that still parses as a
// valid targets response. The status code decides, not the body.
func TestPollAgentErrorStatusWithValidBody(t *testing.T) {
	srv := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, targetsBody(3))
	})
	p := New(discardLogger())

	res, status := p.pollAgent(context.Background(), agentCfg("a1", srv.URL), model.NewInterner())
	if status.Err == "" {
		t.Error("expected an error for HTTP 503 despite a parseable body")
	}
	if res != nil {
		t.Errorf("expected no result, got %d active targets", len(res.Active))
	}
	if status.ActiveTargets != 0 {
		t.Errorf("ActiveTargets = %d, want 0 on failure", status.ActiveTargets)
	}
}

// A failed poll must leave LastSuccess untouched, since staleness is measured
// from the last poll that produced data.
func TestPollAgentFailureLeavesLastSuccessZero(t *testing.T) {
	srv := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	p := New(discardLogger())

	_, status := p.pollAgent(context.Background(), agentCfg("a1", srv.URL), model.NewInterner())
	if !status.LastSuccess.IsZero() {
		t.Error("LastSuccess set after a failed poll")
	}
	if status.LastPoll.IsZero() {
		t.Error("LastPoll not recorded for a failed poll")
	}
	if status.Healthy() {
		t.Error("agent reported healthy after a failed poll")
	}
}

func TestPollAgentUnreachable(t *testing.T) {
	p := New(discardLogger())
	cfg := agentCfg("dead", "http://127.0.0.1:1")
	cfg.Timeout = 500 * time.Millisecond

	if _, status := p.pollAgent(context.Background(), cfg, model.NewInterner()); status.Err == "" {
		t.Error("expected an error for an unreachable agent")
	}
}

// The per-agent timeout must bound a poll against an agent that accepts the
// connection and then stalls.
func TestPollAgentTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	t.Cleanup(func() { close(release) })

	p := New(discardLogger())
	cfg := agentCfg("slow", srv.URL)
	cfg.Timeout = 200 * time.Millisecond

	start := time.Now()
	_, status := p.pollAgent(context.Background(), cfg, model.NewInterner())
	elapsed := time.Since(start)

	if status.Err == "" {
		t.Error("expected a timeout error")
	}
	if elapsed > 3*time.Second {
		t.Errorf("poll took %v; per-agent timeout not applied", elapsed)
	}
}

// Each agent polls on its own schedule, so a stalled agent cannot delay
// another agent's polls.
func TestRunnerSlowAgentDoesNotBlockOthers(t *testing.T) {
	release := make(chan struct{})
	slow := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	t.Cleanup(func() { close(release) })
	fast := okAgent(t, 1)

	slowCfg := agentCfg("slow", slow.URL)
	slowCfg.Timeout = 10 * time.Second
	fastCfg := agentCfg("fast", fast.URL)
	fastCfg.Interval = 100 * time.Millisecond

	c := newCollector()
	r := NewRunner(discardLogger(), c.publish)
	runRunner(t, r, []config.Agent{slowCfg, fastCfg})

	// The fast agent must report repeatedly while the slow one is still stalled.
	deadline := time.After(5 * time.Second)
	for c.countFor("fast") < 3 {
		select {
		case <-c.ping:
		case <-deadline:
			t.Fatalf("fast agent produced %d results while slow agent was blocked",
				c.countFor("fast"))
		}
	}
	if n := c.countFor("slow"); n != 0 {
		t.Errorf("slow agent produced %d results while blocked", n)
	}
}

// Each agent ticks at its own configured interval.
func TestRunnerHonoursPerAgentInterval(t *testing.T) {
	srv := okAgent(t, 1)
	quick := agentCfg("quick", srv.URL)
	quick.Interval = 80 * time.Millisecond
	slow := agentCfg("slow", srv.URL)
	slow.Interval = 2 * time.Second

	c := newCollector()
	r := NewRunner(discardLogger(), c.publish)
	r.Jitter = 0
	runRunner(t, r, []config.Agent{quick, slow})

	time.Sleep(700 * time.Millisecond)

	q, s := c.countFor("quick"), c.countFor("slow")
	if q < 4 {
		t.Errorf("quick agent polled %d times in 700ms at an 80ms interval", q)
	}
	if s > 2 {
		t.Errorf("slow agent polled %d times in 700ms at a 2s interval", s)
	}
}

// Agents must not all fire at the same instant, which would spike load across
// the fleet at every interval boundary.
func TestRunnerStaggersInitialPolls(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	srv := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		fmt.Fprint(w, targetsBody(1))
	})

	var agents []config.Agent
	for i := range 8 {
		a := agentCfg(fmt.Sprintf("a%d", i), srv.URL)
		a.Interval = 2 * time.Second
		agents = append(agents, a)
	}

	c := newCollector()
	r := NewRunner(discardLogger(), c.publish)
	runRunner(t, r, agents)
	c.awaitResults(t, 8)

	mu.Lock()
	defer mu.Unlock()
	first, last := times[0], times[0]
	for _, ts := range times {
		if ts.Before(first) {
			first = ts
		}
		if ts.After(last) {
			last = ts
		}
	}
	if spread := last.Sub(first); spread < 50*time.Millisecond {
		t.Errorf("initial polls spread over %v; agents are not staggered", spread)
	}
}

func TestRunnerReportsFailures(t *testing.T) {
	good := okAgent(t, 2)
	bad := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	})

	goodCfg := agentCfg("good", good.URL)
	goodCfg.Interval = 100 * time.Millisecond
	badCfg := agentCfg("bad", bad.URL)
	badCfg.Interval = 100 * time.Millisecond

	c := newCollector()
	r := NewRunner(discardLogger(), c.publish)
	runRunner(t, r, []config.Agent{goodCfg, badCfg})
	c.awaitResults(t, 2)

	var sawGood, sawBad bool
	for _, res := range c.results() {
		switch res.Status.Name {
		case "good":
			sawGood = true
			if res.Status.Err != "" || res.Result == nil {
				t.Errorf("good agent: err=%q result=%v", res.Status.Err, res.Result)
			}
		case "bad":
			sawBad = true
			if res.Status.Err == "" || res.Result != nil {
				t.Errorf("bad agent: err=%q result=%v", res.Status.Err, res.Result)
			}
		}
	}
	if !sawGood || !sawBad {
		t.Errorf("missing results: good=%v bad=%v", sawGood, sawBad)
	}
}

// A failing agent must keep retrying rather than stopping its goroutine.
func TestRunnerRetriesAfterFailure(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, targetsBody(1))
	})

	cfg := agentCfg("flaky", srv.URL)
	cfg.Interval = 80 * time.Millisecond

	c := newCollector()
	r := NewRunner(discardLogger(), c.publish)
	r.Jitter = 0
	runRunner(t, r, []config.Agent{cfg})

	c.awaitResults(t, 2)
	fail.Store(false)

	deadline := time.After(5 * time.Second)
	for {
		ok := false
		for _, res := range c.results() {
			if res.Status.Err == "" && res.Result != nil {
				ok = true
			}
		}
		if ok {
			return
		}
		select {
		case <-c.ping:
		case <-deadline:
			t.Fatal("agent never recovered after its failures")
		}
	}
}

// Labels equal across agents must be stored once, so the interner is shared.
func TestRunnerSharesInterner(t *testing.T) {
	srv := okAgent(t, 5)
	a1 := agentCfg("a1", srv.URL)
	a2 := agentCfg("a2", srv.URL)

	c := newCollector()
	r := NewRunner(discardLogger(), c.publish)
	// Jitter scales with the interval, so disable it to keep the first poll
	// of each agent prompt.
	r.Jitter = 0
	runRunner(t, r, []config.Agent{a1, a2})
	c.awaitResults(t, 2)

	byName := map[string]AgentResult{}
	for _, res := range c.results() {
		byName[res.Status.Name] = res
	}
	first, second := byName["a1"], byName["a2"]
	if first.Result == nil || second.Result == nil {
		t.Fatal("expected results from both agents")
	}
	shared := r.Interner()
	x := shared.Lookup(first.Result.Active[0].Labels[0].Value)
	y := shared.Lookup(second.Result.Active[0].Labels[0].Value)
	if x != y {
		t.Fatalf("instance labels differ: %q vs %q", x, y)
	}
	if !sameBacking(x, y) {
		t.Error("agents produced separate copies of an identical label value")
	}
}

func TestRunnerStopsOnCancel(t *testing.T) {
	srv := okAgent(t, 1)
	cfg := agentCfg("a1", srv.URL)
	cfg.Interval = 50 * time.Millisecond

	c := newCollector()
	r := NewRunner(discardLogger(), c.publish)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx, []config.Agent{cfg})
		close(done)
	}()
	c.awaitResults(t, 1)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	// No further polls after Run returns.
	before := srv.requests.Load()
	time.Sleep(200 * time.Millisecond)
	if after := srv.requests.Load(); after != before {
		t.Errorf("agent polled %d more times after shutdown", after-before)
	}
}
