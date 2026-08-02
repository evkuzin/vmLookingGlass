package model

import (
	"encoding/binary"
	"hash/maphash"
	"time"
)

// Target health values as reported by vmagent.
const (
	HealthUp      = "up"
	HealthDown    = "down"
	HealthUnknown = "unknown"
)

// keySeed keeps target keys consistent within a process run. Keys are
// identity within one snapshot only and are never persisted or compared
// across restarts.
var keySeed = maphash.MakeSeed()

// TargetKey identifies a physical scrape target independently of which agent
// reported it, so the same target seen by several agents collapses to one row.
type TargetKey uint64

// Target is one scrape target as reported by a single agent.
type Target struct {
	// Agent is the configured name of the reporting vmagent.
	Agent string

	ScrapePool string
	ScrapeURL  string

	// Labels is the label set after relabeling.
	Labels Labels
	// DiscoveredLabels is the label set before relabeling.
	DiscoveredLabels Labels

	Health             string
	LastError          string
	LastScrape         time.Time
	LastScrapeDuration float64
	LastSamplesScraped int64

	// Dropped marks a target that was discovered but relabeled away. vmagent
	// reports these cumulatively rather than as a current snapshot.
	Dropped bool
	// DropReason is set for dropped targets when the agent reports one.
	DropReason string
}

// Key returns the target's cross-agent identity.
//
// For an active target the identity is its scrape URL plus its final labels.
// The URL alone is not sufficient because one URL can back several targets
// with different label sets, and the instance label alone is not unique even
// within a single agent.
//
// A dropped target was relabeled away before it got either, so it is
// identified by the labels it was discovered with instead. The two forms are
// hashed under distinct prefixes so a dropped target cannot collide with an
// active one. Each component is length-prefixed, so a separator occurring
// inside a value cannot forge the digest of a different field split.
func (t *Target) Key() TargetKey {
	var h maphash.Hash
	h.SetSeed(keySeed)

	labels := t.Labels
	if t.Dropped {
		writeLenPrefixed(&h, "dropped")
		labels = t.DiscoveredLabels
	} else {
		writeLenPrefixed(&h, "active")
	}

	writeLenPrefixed(&h, t.ScrapeURL)
	for _, l := range labels {
		writeLenPrefixed(&h, l.Name)
		writeLenPrefixed(&h, l.Value)
	}
	return TargetKey(h.Sum64())
}

func writeLenPrefixed(h *maphash.Hash, s string) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(len(s)))
	_, _ = h.Write(buf[:])
	_, _ = h.WriteString(s)
}

// IsUp reports whether the last scrape succeeded.
func (t *Target) IsUp() bool { return t.Health == HealthUp }

// Instance returns the instance label, the usual human-facing target name.
func (t *Target) Instance() string { return t.Labels.Get("instance") }

// Job returns the job label.
func (t *Target) Job() string { return t.Labels.Get("job") }

// AgentStatus is the outcome of the most recent poll of one agent.
type AgentStatus struct {
	Name string
	URL  string

	// LastPoll is when the last poll completed, successfully or not.
	// The zero value means the agent has not been polled yet.
	LastPoll time.Time
	// LastSuccess is when the agent last returned usable data.
	LastSuccess time.Time
	// Err describes the most recent failure, empty when the last poll worked.
	Err string

	// Interval is the effective poll interval for this agent.
	Interval time.Duration
	// Duration is how long the last poll took.
	Duration time.Duration

	ActiveTargets  int
	DroppedTargets int
	UpTargets      int
	DownTargets    int

	// DroppedTruncated marks that the agent hit its own dropped-target cap,
	// so its dropped list is a partial and arbitrary subset.
	DroppedTruncated bool
	// MaxDroppedTargets is the cap the agent runs with, read from its flags.
	MaxDroppedTargets int
}

// staleFactor is how many intervals may elapse before data is called stale.
// Two allows one missed poll without flapping the indicator.
const staleFactor = 2

// IsStale reports whether the agent's data is too old to trust, which happens
// when polls are failing or an agent is unreachable.
func (s *AgentStatus) IsStale(now time.Time) bool {
	if s.LastSuccess.IsZero() {
		return true
	}
	interval := s.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	return now.Sub(s.LastSuccess) > time.Duration(staleFactor)*interval
}

// Healthy reports whether the last poll succeeded.
func (s *AgentStatus) Healthy() bool { return s.Err == "" && !s.LastSuccess.IsZero() }
