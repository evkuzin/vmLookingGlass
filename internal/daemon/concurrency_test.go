package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/config"
)

// A fleet-sized poll must not open every connection at once: agents that all
// fire together starve each other and time out, even though each responds
// quickly on its own.
func TestConcurrencyIsCapped(t *testing.T) {
	var inFlight, peak atomic.Int64
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		fmt.Fprint(w, targetsBody("h1"))
	}))
	t.Cleanup(func() { srv.Close() })

	var agents []config.Agent
	for i := range 20 {
		a := agent(fmt.Sprintf("a%02d", i), srv.URL)
		a.Timeout = 30 * time.Second
		agents = append(agents, a)
	}

	c := cfg(agents...)
	c.MaxConcurrency = 4

	d := New(discardLogger())
	run(t, d, c)

	// Give every agent time to have started if it were going to.
	time.Sleep(700 * time.Millisecond)
	got := peak.Load()
	close(release)

	if got > 4 {
		t.Errorf("peak concurrent polls = %d, want at most the configured 4", got)
	}
	if got == 0 {
		t.Error("no polls started")
	}
}

// Without a configured cap the daemon still bounds itself, so a large fleet
// does not open one connection per agent simultaneously.
func TestDefaultConcurrencyIsBounded(t *testing.T) {
	var inFlight, peak atomic.Int64
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		fmt.Fprint(w, targetsBody("h1"))
	}))
	t.Cleanup(func() { srv.Close() })

	var agents []config.Agent
	for i := range 60 {
		a := agent(fmt.Sprintf("a%02d", i), srv.URL)
		a.Timeout = 30 * time.Second
		agents = append(agents, a)
	}

	d := New(discardLogger())
	run(t, d, cfg(agents...))

	time.Sleep(700 * time.Millisecond)
	got := peak.Load()
	close(release)

	if got >= 60 {
		t.Errorf("peak concurrent polls = %d; the daemon is not bounding itself", got)
	}
}

// One HTTP client is shared, so connections are pooled across agents rather
// than each agent keeping its own pool.
func TestAgentsShareHTTPClient(t *testing.T) {
	s1, _ := agentServer(t, "h1")
	s2, _ := agentServer(t, "h2")

	d := New(discardLogger())
	run(t, d, cfg(agent("a1", s1.URL), agent("a2", s2.URL)))
	awaitTargets(t, d, 2)

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.running) != 2 {
		t.Fatalf("running = %d agents, want 2", len(d.running))
	}
	// Every agent's runner must be backed by the daemon's single poller.
	for name, task := range d.running {
		if task.runner.Poller() != d.poller {
			t.Errorf("agent %s has its own poller instead of the shared one", name)
		}
	}
}

// The cap must apply across a reload, not just at startup.
func TestConcurrencyCapSurvivesReload(t *testing.T) {
	var inFlight, peak atomic.Int64
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		fmt.Fprint(w, targetsBody("h1"))
	}))
	t.Cleanup(func() { srv.Close() })

	mk := func(n int) *config.Config {
		var agents []config.Agent
		for i := range n {
			a := agent(fmt.Sprintf("a%02d", i), srv.URL)
			a.Timeout = 30 * time.Second
			agents = append(agents, a)
		}
		c := cfg(agents...)
		c.MaxConcurrency = 3
		return c
	}

	d := New(discardLogger())
	run(t, d, mk(4))
	time.Sleep(200 * time.Millisecond)

	d.Reload(mk(20))
	time.Sleep(700 * time.Millisecond)

	got := peak.Load()
	close(release)
	if got > 3 {
		t.Errorf("peak concurrent polls after reload = %d, want at most 3", got)
	}
}
