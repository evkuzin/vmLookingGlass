package model

import (
	"testing"
	"time"
)

func TestTargetKeyDistinguishesByURL(t *testing.T) {
	in := NewInterner()
	labels := in.Labels(map[string]string{"instance": "host:9122", "job": "redis"})

	a := Target{ScrapeURL: "http://10.0.0.1:9122/metrics", Labels: labels}
	b := Target{ScrapeURL: "http://10.0.0.2:9122/metrics", Labels: labels}

	if a.Key() == b.Key() {
		t.Error("targets with the same labels but different URLs share a key")
	}
}

// The instance label is not unique even within one agent, so identity must
// also account for the rest of the label set.
func TestTargetKeyDistinguishesByLabels(t *testing.T) {
	in := NewInterner()
	const url = "http://10.0.0.1:9122/metrics"

	a := Target{ScrapeURL: url, Labels: in.Labels(map[string]string{"instance": "host:9122", "job": "redis-a"})}
	b := Target{ScrapeURL: url, Labels: in.Labels(map[string]string{"instance": "host:9122", "job": "redis-b"})}

	if a.Key() == b.Key() {
		t.Error("targets differing only by job label share a key")
	}
}

func TestTargetKeyStableAcrossAgents(t *testing.T) {
	in := NewInterner()
	const url = "http://10.0.0.1:9122/metrics"
	labels := map[string]string{"instance": "host:9122", "job": "redis"}

	// Same physical target seen by two agents: identity must not include the agent.
	a := Target{Agent: "agent01", ScrapeURL: url, Labels: in.Labels(labels)}
	b := Target{Agent: "agent02", ScrapeURL: url, Labels: in.Labels(labels)}

	if a.Key() != b.Key() {
		t.Error("same target scraped by two agents produced different keys")
	}
}

// Label insertion order must not affect identity.
func TestTargetKeyOrderIndependent(t *testing.T) {
	in := NewInterner()
	const url = "http://h:1/metrics"

	a := Target{ScrapeURL: url, Labels: in.Labels(map[string]string{"a": "1", "b": "2", "c": "3"})}
	b := Target{ScrapeURL: url, Labels: in.Labels(map[string]string{"c": "3", "a": "1", "b": "2"})}

	if a.Key() != b.Key() {
		t.Error("key depends on label insertion order")
	}
}

// Key must hash field boundaries, not just field contents. These two targets
// serialize to the same byte stream under any scheme that concatenates
// names and values without encoding their lengths.
func TestTargetKeyNoSeparatorCollision(t *testing.T) {
	in := NewInterner()
	const url = "http://h/m"

	// Concatenated: "a" + "bc" + "d" + "e"
	a := Target{ScrapeURL: url, Labels: in.Labels(map[string]string{"a": "bc", "d": "e"})}
	// Concatenated: "ab" + "c" + "d" + "e" — identical stream, different labels.
	b := Target{ScrapeURL: url, Labels: in.Labels(map[string]string{"ab": "c", "d": "e"})}

	if a.Key() == b.Key() {
		t.Errorf("distinct label sets collide: %v vs %v", a.Labels, b.Labels)
	}
}

// Dropped targets have no scrape URL and no post-relabel labels, so identity
// has to fall back to the discovered labels or every dropped target in the
// fleet would collapse into one.
func TestDroppedTargetKeyUsesDiscoveredLabels(t *testing.T) {
	in := NewInterner()
	mk := func(addr string) Target {
		return Target{
			Dropped:          true,
			DiscoveredLabels: in.Labels(map[string]string{"__address__": addr, "job": "pool-9"}),
		}
	}
	a, b := mk("hostA:9119"), mk("hostB:9119")

	if a.Key() == b.Key() {
		t.Error("dropped targets with different addresses share a key")
	}
	same := mk("hostA:9119")
	if a.Key() != same.Key() {
		t.Error("identical dropped targets produced different keys")
	}
}

// A dropped target must not collide with an active one that happens to have
// no labels yet.
func TestDroppedAndActiveKeysDiffer(t *testing.T) {
	in := NewInterner()
	dropped := Target{
		Dropped:          true,
		DiscoveredLabels: in.Labels(map[string]string{"__address__": "h:9100"}),
	}
	active := Target{
		ScrapeURL:        "http://h:9100/metrics",
		DiscoveredLabels: in.Labels(map[string]string{"__address__": "h:9100"}),
	}
	if dropped.Key() == active.Key() {
		t.Error("a dropped target collided with an active one")
	}
}

func TestTargetHealthHelpers(t *testing.T) {
	tests := []struct {
		health string
		up     bool
	}{
		{HealthUp, true},
		{HealthDown, false},
		{HealthUnknown, false},
	}
	for _, tt := range tests {
		tgt := Target{Health: tt.health}
		if got := tgt.IsUp(); got != tt.up {
			t.Errorf("health %q: IsUp = %v, want %v", tt.health, got, tt.up)
		}
	}
}

func TestAgentStatusStale(t *testing.T) {
	now := time.Now()
	fresh := AgentStatus{LastSuccess: now.Add(-10 * time.Second), Interval: time.Minute}
	stale := AgentStatus{LastSuccess: now.Add(-5 * time.Minute), Interval: time.Minute}

	if fresh.IsStale(now) {
		t.Error("agent polled 10s ago with a 1m interval reported stale")
	}
	if !stale.IsStale(now) {
		t.Error("agent polled 5m ago with a 1m interval not reported stale")
	}
}

// An agent polled constantly but failing every time is stale: staleness
// tracks the age of usable data, not of the last attempt.
func TestAgentStatusStaleWhenPollsKeepFailing(t *testing.T) {
	now := time.Now()
	s := AgentStatus{
		LastPoll:    now.Add(-1 * time.Second),
		LastSuccess: now.Add(-10 * time.Minute),
		Err:         "connection refused",
		Interval:    time.Minute,
	}
	if !s.IsStale(now) {
		t.Error("agent with recent failed polls but old data not reported stale")
	}
	if s.Healthy() {
		t.Error("agent with an error reported healthy")
	}
}

func TestAgentStatusNeverPolled(t *testing.T) {
	s := AgentStatus{Interval: time.Minute}
	if !s.IsStale(time.Now()) {
		t.Error("agent that has never been polled should be stale")
	}
}
