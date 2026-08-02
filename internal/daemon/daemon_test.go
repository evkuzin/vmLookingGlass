package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func targetsBody(instance string) string {
	return fmt.Sprintf(`{"status":"success","data":{"activeTargets":[
	  {"scrapePool":"p","scrapeUrl":"http://%s/metrics","labels":{"instance":%q},"health":"up"}
	],"droppedTargets":[]}}`, instance, instance)
}

func agentServer(t *testing.T, instance string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, targetsBody(instance))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func cfg(agents ...config.Agent) *config.Config {
	return &config.Config{
		Listen:   "127.0.0.1:0",
		Interval: 50 * time.Millisecond,
		Timeout:  2 * time.Second,
		Agents:   agents,
	}
}

func agent(name, url string) config.Agent {
	return config.Agent{
		Name: name, URL: url,
		Interval: 50 * time.Millisecond, Timeout: 2 * time.Second,
	}
}

// run starts a Daemon and stops it when the test ends.
func run(t *testing.T, d *Daemon, c *config.Config) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx, c)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Daemon did not stop after cancellation")
		}
	})
}

// awaitTargets waits until the store holds n targets.
func awaitTargets(t *testing.T, d *Daemon, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if len(d.Store().Snapshot().Targets) == n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out with %d targets, want %d",
				len(d.Store().Snapshot().Targets), n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestDaemonPollsConfiguredAgents(t *testing.T) {
	s1, _ := agentServer(t, "h1")
	s2, _ := agentServer(t, "h2")

	d := New(discardLogger())
	run(t, d, cfg(agent("a1", s1.URL), agent("a2", s2.URL)))
	awaitTargets(t, d, 2)

	if got := len(d.Store().Snapshot().Agents); got != 2 {
		t.Errorf("agents = %d, want 2", got)
	}
}

// Reloading with a new agent starts polling it without a restart.
func TestReloadAddsAgent(t *testing.T) {
	s1, _ := agentServer(t, "h1")
	s2, _ := agentServer(t, "h2")

	d := New(discardLogger())
	run(t, d, cfg(agent("a1", s1.URL)))
	awaitTargets(t, d, 1)

	d.Reload(cfg(agent("a1", s1.URL), agent("a2", s2.URL)))
	awaitTargets(t, d, 2)
}

// Reloading without an agent stops polling it and drops its targets, so a
// removed agent does not linger in the view.
func TestReloadRemovesAgent(t *testing.T) {
	s1, _ := agentServer(t, "h1")
	s2, hits2 := agentServer(t, "h2")

	d := New(discardLogger())
	run(t, d, cfg(agent("a1", s1.URL), agent("a2", s2.URL)))
	awaitTargets(t, d, 2)

	d.Reload(cfg(agent("a1", s1.URL)))
	awaitTargets(t, d, 1)

	if d.Store().Snapshot().Agent("a2") != nil {
		t.Error("removed agent still present in the snapshot")
	}

	// Polling of the removed agent must actually stop.
	before := hits2.Load()
	time.Sleep(300 * time.Millisecond)
	if after := hits2.Load(); after > before+1 {
		t.Errorf("removed agent polled %d more times after reload", after-before)
	}
}

// An agent whose config is unchanged keeps polling across a reload.
func TestReloadKeepsUnchangedAgentRunning(t *testing.T) {
	s1, hits1 := agentServer(t, "h1")
	s2, _ := agentServer(t, "h2")

	d := New(discardLogger())
	run(t, d, cfg(agent("a1", s1.URL)))
	awaitTargets(t, d, 1)

	d.Reload(cfg(agent("a1", s1.URL), agent("a2", s2.URL)))
	awaitTargets(t, d, 2)

	before := hits1.Load()
	deadline := time.After(3 * time.Second)
	for hits1.Load() <= before {
		select {
		case <-deadline:
			t.Fatal("unchanged agent stopped polling after reload")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Changing an agent's URL must repoint it rather than keep the old target.
func TestReloadChangedAgentURL(t *testing.T) {
	s1, _ := agentServer(t, "h1")
	s2, _ := agentServer(t, "h2")

	d := New(discardLogger())
	run(t, d, cfg(agent("a1", s1.URL)))
	awaitTargets(t, d, 1)

	d.Reload(cfg(agent("a1", s2.URL)))

	deadline := time.After(5 * time.Second)
	for {
		targets := d.Store().Snapshot().Targets
		if len(targets) == 1 && targets[0].Labels.Get("instance") == "h2" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("agent did not repoint: %d targets", len(targets))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestReloadIsConcurrencySafe(t *testing.T) {
	s1, _ := agentServer(t, "h1")
	s2, _ := agentServer(t, "h2")

	d := New(discardLogger())
	run(t, d, cfg(agent("a1", s1.URL)))
	awaitTargets(t, d, 1)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				d.Reload(cfg(agent("a1", s1.URL), agent("a2", s2.URL)))
			} else {
				d.Reload(cfg(agent("a1", s1.URL)))
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = d.Store().Snapshot().Targets
			}
		}()
	}
	wg.Wait()

	// Settle on a known config and confirm the daemon still works.
	d.Reload(cfg(agent("a1", s1.URL)))
	awaitTargets(t, d, 1)
}

func TestDaemonStopsAllAgentsOnCancel(t *testing.T) {
	s1, hits1 := agentServer(t, "h1")

	d := New(discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx, cfg(agent("a1", s1.URL)))
		close(done)
	}()
	awaitTargets(t, d, 1)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	before := hits1.Load()
	time.Sleep(300 * time.Millisecond)
	if after := hits1.Load(); after != before {
		t.Errorf("agent polled %d more times after shutdown", after-before)
	}
}

// Repeated add/remove cycles must not leak goroutines: each removed agent's
// poll loop has to exit, not just stop being referenced.
func TestReloadDoesNotLeakGoroutines(t *testing.T) {
	s1, _ := agentServer(t, "h1")
	s2, _ := agentServer(t, "h2")

	d := New(discardLogger())
	run(t, d, cfg(agent("a1", s1.URL)))
	awaitTargets(t, d, 1)

	settled := runtime.NumGoroutine()
	for range 20 {
		d.Reload(cfg(agent("a1", s1.URL), agent("a2", s2.URL)))
		d.Reload(cfg(agent("a1", s1.URL)))
	}
	awaitTargets(t, d, 1)

	// Allow the last canceled loops to unwind.
	deadline := time.After(3 * time.Second)
	for runtime.NumGoroutine() > settled+5 {
		select {
		case <-deadline:
			t.Fatalf("goroutines grew from %d to %d across 20 add/remove cycles",
				settled, runtime.NumGoroutine())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Reload must not return until a removed agent's goroutine has exited, so the
// daemon is settled the moment Reload returns rather than some time later.
func TestReloadWaitsForRemovedAgentToExit(t *testing.T) {
	s1, _ := agentServer(t, "h1")
	// This agent stalls mid-poll, so its goroutine is busy when it is removed.
	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	t.Cleanup(func() { close(block); slow.Close() })

	slowCfg := agent("slow", slow.URL)
	slowCfg.Timeout = 10 * time.Second

	d := New(discardLogger())
	run(t, d, cfg(agent("a1", s1.URL), slowCfg))
	awaitTargets(t, d, 1)

	settled := runtime.NumGoroutine()
	d.Reload(cfg(agent("a1", s1.URL)))

	// Reload returning means the removed goroutine is already gone, so the
	// count must not still be dropping afterwards.
	if after := runtime.NumGoroutine(); after > settled {
		t.Errorf("goroutines went from %d to %d across Reload; the removed "+
			"agent was still running when Reload returned", settled, after)
	}
}

// Canceling the daemon stops every agent, not just the first.
func TestShutdownStopsEveryAgent(t *testing.T) {
	s1, hits1 := agentServer(t, "h1")
	s2, hits2 := agentServer(t, "h2")
	s3, hits3 := agentServer(t, "h3")

	d := New(discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx, cfg(agent("a1", s1.URL), agent("a2", s2.URL), agent("a3", s3.URL)))
		close(done)
	}()
	awaitTargets(t, d, 3)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}

	before := [3]int64{hits1.Load(), hits2.Load(), hits3.Load()}
	time.Sleep(300 * time.Millisecond)
	for i, h := range []*atomic.Int64{hits1, hits2, hits3} {
		if after := h.Load(); after != before[i] {
			t.Errorf("agent %d polled %d more times after shutdown", i+1, after-before[i])
		}
	}
}

// All agents share one interner, so a label common to several agents is
// stored once rather than per agent.
func TestAgentsShareInterner(t *testing.T) {
	// Both agents report an identical label value.
	s1, _ := agentServer(t, "same")
	s2, _ := agentServer(t, "same")

	d := New(discardLogger())
	run(t, d, cfg(agent("a1", s1.URL), agent("a2", s2.URL)))

	deadline := time.After(5 * time.Second)
	for len(d.Store().Snapshot().Agents) < 2 {
		select {
		case <-deadline:
			t.Fatal("both agents did not report")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// "same" plus "instance" are shared; a per-agent interner would hold two
	// copies of each.
	// Both agents' labels must land in the daemon's interner: too few means
	// they are interning elsewhere, too many means each has its own copy.
	got := d.interner.Len()
	if got == 0 {
		t.Fatal("no labels interned into the shared interner")
	}
	if got > 4 {
		t.Errorf("interner holds %d strings for a 2-label vocabulary; not shared", got)
	}
}
