package store

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/model"
	"github.com/evkuzin/vmLookingGlass/internal/poll"
)

func target(in *model.Interner, agent, url string, labels map[string]string) model.Target {
	return model.Target{
		Agent:      agent,
		ScrapePool: "p",
		ScrapeURL:  url,
		Labels:     in.Labels(labels),
		Health:     model.HealthUp,
	}
}

// result builds an AgentResult for an agent that polled successfully.
func result(name string, active ...model.Target) poll.AgentResult {
	up := 0
	for i := range active {
		if active[i].IsUp() {
			up++
		}
	}
	return poll.AgentResult{
		Result: &poll.Result{Active: active},
		Status: model.AgentStatus{
			Name:          name,
			LastPoll:      time.Now(),
			LastSuccess:   time.Now(),
			Interval:      time.Minute,
			ActiveTargets: len(active),
			UpTargets:     up,
		},
	}
}

func failure(name, err string) poll.AgentResult {
	return poll.AgentResult{
		Status: model.AgentStatus{
			Name:     name,
			LastPoll: time.Now(),
			Interval: time.Minute,
			Err:      err,
		},
	}
}

func TestEmptyStore(t *testing.T) {
	s := New()
	snap := s.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil before any updates")
	}
	if len(snap.Targets) != 0 || len(snap.Agents) != 0 {
		t.Errorf("empty store has %d targets, %d agents", len(snap.Targets), len(snap.Agents))
	}
}

func TestApplySingleAgent(t *testing.T) {
	in := model.NewInterner()
	s := New()
	s.Apply(result("a1",
		target(in, "a1", "http://h1/metrics", map[string]string{"instance": "h1", "job": "node"}),
		target(in, "a1", "http://h2/metrics", map[string]string{"instance": "h2", "job": "node"}),
	))
	s.Flush()

	snap := s.Snapshot()
	if len(snap.Targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(snap.Targets))
	}
	if len(snap.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(snap.Agents))
	}
	for _, tgt := range snap.Targets {
		if len(tgt.Agents) != 1 || tgt.Agents[0] != "a1" {
			t.Errorf("target %s: agents = %v, want [a1]", tgt.ScrapeURL, tgt.Agents)
		}
		if tgt.MultiScraped() {
			t.Errorf("target %s wrongly flagged as multi-scraped", tgt.ScrapeURL)
		}
	}
}

// The same physical target reported by two agents collapses to one row that
// names both, which is how accidental double-scraping becomes visible.
func TestTargetScrapedByTwoAgents(t *testing.T) {
	in := model.NewInterner()
	labels := map[string]string{"instance": "h1:9100", "job": "node"}
	const url = "http://10.0.0.1:9100/metrics"

	s := New()
	s.Apply(result("a1", target(in, "a1", url, labels)))
	s.Apply(result("a2", target(in, "a2", url, labels)))
	s.Flush()

	snap := s.Snapshot()
	if len(snap.Targets) != 1 {
		t.Fatalf("targets = %d, want 1 merged row", len(snap.Targets))
	}
	tgt := snap.Targets[0]
	if len(tgt.Agents) != 2 {
		t.Fatalf("agents = %v, want both", tgt.Agents)
	}
	if !tgt.MultiScraped() {
		t.Error("target scraped by two agents not flagged")
	}
	// Agent order must be stable regardless of arrival order.
	if tgt.Agents[0] != "a1" || tgt.Agents[1] != "a2" {
		t.Errorf("agents = %v, want sorted [a1 a2]", tgt.Agents)
	}
}

// Re-polling an agent must not add it to a target's agent list twice.
func TestRepeatedPollDoesNotDuplicateAgent(t *testing.T) {
	in := model.NewInterner()
	labels := map[string]string{"instance": "h1:9100"}
	const url = "http://10.0.0.1:9100/metrics"

	s := New()
	for range 3 {
		s.Apply(result("a1", target(in, "a1", url, labels)))
	}
	s.Flush()

	tgt := s.Snapshot().Targets[0]
	if len(tgt.Agents) != 1 {
		t.Errorf("agents = %v, want a single entry after repeated polls", tgt.Agents)
	}
	if tgt.MultiScraped() {
		t.Error("target flagged multi-scraped after repeated polls by one agent")
	}
}

// An agent can report the same target twice in one response, either as a
// duplicate discovery result or as both an active and a dropped entry. That
// must not make one agent look like two.
func TestDuplicateTargetWithinOneAgent(t *testing.T) {
	in := model.NewInterner()
	labels := map[string]string{"instance": "h1:9100"}
	const url = "http://10.0.0.1:9100/metrics"

	s := New()
	dup := target(in, "a1", url, labels)
	res := result("a1", dup, dup)
	res.Result.Dropped = []model.Target{dup}
	s.Apply(res)
	s.Flush()

	snap := s.Snapshot()
	if len(snap.Targets) != 1 {
		t.Fatalf("targets = %d, want 1 merged row", len(snap.Targets))
	}
	tgt := snap.Targets[0]
	if len(tgt.Agents) != 1 {
		t.Errorf("agents = %v, want a single entry", tgt.Agents)
	}
	if tgt.MultiScraped() {
		t.Error("one agent reporting a target twice flagged as multi-scraped")
	}
}

// Target order must be stable across rebuilds, or paging through results
// would skip and repeat rows as agents report.
func TestTargetOrderStableAcrossRebuilds(t *testing.T) {
	in := model.NewInterner()
	s := New()

	var targets []model.Target
	for i := range 20 {
		url := fmt.Sprintf("http://h%02d/metrics", i)
		targets = append(targets, target(in, "a1", url, map[string]string{"instance": url}))
	}
	s.Apply(result("a1", targets...))
	s.Flush()

	first := urls(s.Snapshot())
	for range 5 {
		// Rebuild without changing anything an agent reports.
		s.Apply(result("a1", targets...))
		s.Flush()
		if got := urls(s.Snapshot()); !slices.Equal(got, first) {
			t.Fatalf("target order changed between rebuilds:\n %v\n %v", first, got)
		}
	}
	if !slices.IsSorted(first) {
		t.Errorf("targets not in sorted order: %v", first)
	}
}

func urls(s *Snapshot) []string {
	out := make([]string, 0, len(s.Targets))
	for _, e := range s.Targets {
		out = append(out, e.ScrapeURL)
	}
	return out
}

func TestMergeOrderIndependent(t *testing.T) {
	in := model.NewInterner()
	labels := map[string]string{"instance": "h1:9100"}
	const url = "http://10.0.0.1:9100/metrics"

	forward := New()
	forward.Apply(result("a1", target(in, "a1", url, labels)))
	forward.Apply(result("a2", target(in, "a2", url, labels)))

	reverse := New()
	reverse.Apply(result("a2", target(in, "a2", url, labels)))
	reverse.Apply(result("a1", target(in, "a1", url, labels)))
	forward.Flush()
	reverse.Flush()

	f, r := forward.Snapshot().Targets[0], reverse.Snapshot().Targets[0]
	if len(f.Agents) != len(r.Agents) {
		t.Fatalf("agent counts differ: %v vs %v", f.Agents, r.Agents)
	}
	for i := range f.Agents {
		if f.Agents[i] != r.Agents[i] {
			t.Errorf("agent order differs: %v vs %v", f.Agents, r.Agents)
		}
	}
}

// Re-polling an agent replaces its targets rather than accumulating them.
func TestApplyReplacesAgentTargets(t *testing.T) {
	in := model.NewInterner()
	s := New()
	s.Apply(result("a1",
		target(in, "a1", "http://h1/metrics", map[string]string{"instance": "h1"}),
		target(in, "a1", "http://h2/metrics", map[string]string{"instance": "h2"}),
	))
	// Second poll sees only one target: the other was removed from discovery.
	s.Apply(result("a1",
		target(in, "a1", "http://h1/metrics", map[string]string{"instance": "h1"}),
	))

	s.Flush()
	snap := s.Snapshot()
	if len(snap.Targets) != 1 {
		t.Errorf("targets = %d, want 1 after the target disappeared", len(snap.Targets))
	}
}

// A failed poll must not erase the data from that agent's last good poll.
func TestFailedPollKeepsLastGoodData(t *testing.T) {
	in := model.NewInterner()
	s := New()
	s.Apply(result("a1", target(in, "a1", "http://h1/metrics", map[string]string{"instance": "h1"})))
	s.Apply(failure("a1", "connection refused"))
	s.Flush()

	snap := s.Snapshot()
	if len(snap.Targets) != 1 {
		t.Errorf("targets = %d, want the previous poll's data retained", len(snap.Targets))
	}
	ag := snap.Agent("a1")
	if ag == nil {
		t.Fatal("agent status missing")
	}
	if ag.Err == "" {
		t.Error("agent error not recorded")
	}
	if ag.LastSuccess.IsZero() {
		t.Error("LastSuccess from the earlier good poll was lost")
	}
}

// One agent failing must not affect another agent's targets.
func TestFailureIsolatedToOneAgent(t *testing.T) {
	in := model.NewInterner()
	s := New()
	s.Apply(result("a1", target(in, "a1", "http://h1/metrics", map[string]string{"instance": "h1"})))
	s.Apply(result("a2", target(in, "a2", "http://h2/metrics", map[string]string{"instance": "h2"})))
	s.Apply(failure("a1", "boom"))
	s.Flush()

	snap := s.Snapshot()
	if len(snap.Targets) != 2 {
		t.Errorf("targets = %d, want both agents' data", len(snap.Targets))
	}
	if got := snap.Agent("a2"); got.Err != "" {
		t.Errorf("healthy agent shows error %q", got.Err)
	}
}

func TestScrapePoolsSortedAndUnique(t *testing.T) {
	in := model.NewInterner()
	mk := func(agent, pool, url string) model.Target {
		tgt := target(in, agent, url, map[string]string{"instance": url})
		tgt.ScrapePool = pool
		return tgt
	}
	s := New()
	s.Apply(result("a1", mk("a1", "zebra", "http://h1/m"), mk("a1", "alpha", "http://h2/m")))
	s.Apply(result("a2", mk("a2", "alpha", "http://h3/m"), mk("a2", "middle", "http://h4/m")))
	s.Flush()

	got := s.Snapshot().ScrapePools
	want := []string{"alpha", "middle", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("pools = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pools = %v, want %v", got, want)
			break
		}
	}
}

func TestSnapshotCounts(t *testing.T) {
	in := model.NewInterner()
	down := target(in, "a1", "http://h2/metrics", map[string]string{"instance": "h2"})
	down.Health = model.HealthDown
	dropped := target(in, "a1", "http://h3/metrics", map[string]string{"instance": "h3"})
	dropped.Dropped = true

	s := New()
	res := result("a1", target(in, "a1", "http://h1/metrics", map[string]string{"instance": "h1"}), down)
	res.Result.Dropped = []model.Target{dropped}
	s.Apply(res)
	s.Flush()

	snap := s.Snapshot()
	if snap.ActiveCount != 2 {
		t.Errorf("ActiveCount = %d, want 2", snap.ActiveCount)
	}
	if snap.DroppedCount != 1 {
		t.Errorf("DroppedCount = %d, want 1", snap.DroppedCount)
	}
	if snap.UpCount != 1 || snap.DownCount != 1 {
		t.Errorf("up/down = %d/%d, want 1/1", snap.UpCount, snap.DownCount)
	}
}

// Snapshots are immutable: a reader holding one is unaffected by later polls.
func TestSnapshotIsImmutable(t *testing.T) {
	in := model.NewInterner()
	s := New()
	s.Apply(result("a1", target(in, "a1", "http://h1/metrics", map[string]string{"instance": "h1"})))

	s.Flush()
	held := s.Snapshot()
	before := len(held.Targets)

	s.Apply(result("a2", target(in, "a2", "http://h2/metrics", map[string]string{"instance": "h2"})))

	if after := len(held.Targets); after != before {
		t.Errorf("held snapshot changed from %d to %d targets", before, after)
	}
	s.Flush()
	if now := len(s.Snapshot().Targets); now != 2 {
		t.Errorf("current snapshot has %d targets, want 2", now)
	}
}

func TestSnapshotTakenAt(t *testing.T) {
	s := New()
	before := time.Now()
	s.Apply(result("a1"))
	s.Flush()
	snap := s.Snapshot()
	if snap.TakenAt.Before(before) {
		t.Errorf("TakenAt %v predates the update at %v", snap.TakenAt, before)
	}
}

// Removing an agent from config must drop its targets from the snapshot.
func TestRetainAgents(t *testing.T) {
	in := model.NewInterner()
	s := New()
	s.Apply(result("a1", target(in, "a1", "http://h1/metrics", map[string]string{"instance": "h1"})))
	s.Apply(result("a2", target(in, "a2", "http://h2/metrics", map[string]string{"instance": "h2"})))

	s.RetainAgents([]string{"a1"})
	s.Flush()

	snap := s.Snapshot()
	if len(snap.Targets) != 1 {
		t.Errorf("targets = %d, want only the retained agent's", len(snap.Targets))
	}
	if len(snap.Agents) != 1 || snap.Agents[0].Name != "a1" {
		t.Errorf("agents = %v, want only a1", snap.Agents)
	}
	if snap.Agent("a2") != nil {
		t.Error("removed agent still present")
	}
}

func TestAgentsSortedByName(t *testing.T) {
	s := New()
	for _, n := range []string{"zulu", "alpha", "mike"} {
		s.Apply(result(n))
	}
	s.Flush()
	got := s.Snapshot().Agents
	want := []string{"alpha", "mike", "zulu"}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("agent %d = %q, want %q", i, got[i].Name, want[i])
		}
	}
}

func TestConcurrentApplyAndRead(t *testing.T) {
	in := model.NewInterner()
	s := New()

	var wg sync.WaitGroup
	for a := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("agent%d", a)
			for i := range 50 {
				url := fmt.Sprintf("http://h%d-%d/metrics", a, i)
				s.Apply(result(name, target(in, name, url, map[string]string{"instance": url})))
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				s.Flush()
				snap := s.Snapshot()
				for _, tgt := range snap.Targets {
					_ = tgt.MultiScraped()
				}
			}
		}()
	}
	wg.Wait()

	if len(s.Snapshot().Agents) != 4 {
		t.Errorf("agents = %d, want 4", len(s.Snapshot().Agents))
	}
}

// Rebuilding the whole index costs O(targets), so a burst of agents reporting
// at once must not trigger one rebuild each.
func TestPublishIsCoalesced(t *testing.T) {
	in := model.NewInterner()
	s := New()

	// Hold the first rebuild open so the rest of the burst is guaranteed to
	// arrive while one is in flight, which is the case coalescing exists for.
	// Without the gate the applies can finish one at a time and each
	// legitimately gets its own rebuild.
	var built atomic.Int64
	release := make(chan struct{})
	var once sync.Once
	s.onPublish = func() {
		built.Add(1)
		once.Do(func() { <-release })
	}

	// The first apply starts a rebuild, which then blocks.
	s.Apply(result("agent00", target(in, "agent00", "http://h00/metrics",
		map[string]string{"instance": "http://h00/metrics"})))

	var wg sync.WaitGroup
	for a := 1; a < 20; a++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("agent%02d", a)
			url := fmt.Sprintf("http://h%02d/metrics", a)
			s.Apply(result(name, target(in, name, url, map[string]string{"instance": url})))
		}()
	}
	wg.Wait()
	close(release)
	s.Flush()

	// Every agent's data must be visible even though rebuilds were coalesced.
	if got := len(s.Snapshot().Targets); got != 20 {
		t.Errorf("targets = %d, want 20", got)
	}
	// The 19 applies that landed during the held rebuild collapse into one.
	if got := built.Load(); got > 3 {
		t.Errorf("rebuilt %d times for a burst of 20 applies; not coalesced", got)
	}
}

// Coalescing must not lose the last write: a snapshot taken after the burst
// settles has to include it.
func TestCoalescedPublishEventuallyIncludesLastWrite(t *testing.T) {
	in := model.NewInterner()
	s := New()

	for i := range 10 {
		url := fmt.Sprintf("http://h%02d/metrics", i)
		s.Apply(result("a1", target(in, "a1", url, map[string]string{"instance": url})))
	}
	s.Flush()

	// The final Apply replaced a1's targets with a single one.
	if got := len(s.Snapshot().Targets); got != 1 {
		t.Errorf("targets = %d, want the last write's single target", got)
	}
}
