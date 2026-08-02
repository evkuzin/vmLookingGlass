// Package store holds the aggregated view of every agent's scrape targets.
package store

import (
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/model"
	"github.com/evkuzin/vmLookingGlass/internal/poll"
)

// Entry is one physical scrape target and the agents scraping it.
//
// Agents polling on independent schedules report the same target at different
// times, so the entry keeps the most recently observed copy and the full list
// of agents that have it.
type Entry struct {
	model.Target

	// Agents names every agent scraping this target, sorted.
	Agents []string
	// HealthByAgent records what each agent last saw, which is the only way
	// to tell a target that is down from one that is merely unreachable from
	// part of the fleet.
	HealthByAgent map[string]string
}

// MultiScraped reports whether more than one agent scrapes this target, which
// usually means overlapping discovery rules.
func (e *Entry) MultiScraped() bool { return len(e.Agents) > 1 }

// Disputed reports whether the agents scraping this target disagree about its
// health, which points at a network fault between some agents and the target
// rather than at the target itself.
func (e *Entry) Disputed() bool {
	var first string
	for _, h := range e.HealthByAgent {
		if first == "" {
			first = h
			continue
		}
		if h != first {
			return true
		}
	}
	return false
}

// Snapshot is an immutable view of the fleet at one instant. Readers may hold
// one for as long as they like; it is never mutated after publication.
type Snapshot struct {
	TakenAt time.Time

	// Targets holds every known target, merged across agents.
	Targets []*Entry
	// Agents holds per-agent poll status, sorted by name.
	Agents []model.AgentStatus
	// ScrapePools lists every pool name seen, sorted and deduplicated.
	ScrapePools []string

	ActiveCount  int
	DroppedCount int
	UpCount      int
	DownCount    int

	byAgent map[string]int
	history map[string][]PollRecord
}

// History returns the agent's recent poll outcomes, oldest first, or nil if
// the agent is unknown.
func (s *Snapshot) History(agent string) []PollRecord { return s.history[agent] }

// Agent returns the status for the named agent, or nil if absent.
func (s *Snapshot) Agent(name string) *model.AgentStatus {
	i, ok := s.byAgent[name]
	if !ok {
		return nil
	}
	return &s.Agents[i]
}

// HistoryWindow bounds how far back per-agent poll history is kept. It is
// held only in memory, so a restart starts the record over.
const HistoryWindow = 12 * time.Hour

// HistoryLimit caps entries per agent, so a short poll interval cannot grow
// the history without bound inside the window.
const HistoryLimit = 720

// PollRecord is the outcome of one poll, for showing an agent's recent
// availability.
type PollRecord struct {
	At time.Time
	OK bool
}

// agentData is one agent's most recent successful poll plus its latest status.
type agentData struct {
	status  model.AgentStatus
	active  []model.Target
	dropped []model.Target
	// history is oldest-first, trimmed to the window on each poll.
	history []PollRecord
}

// Store accumulates per-agent poll results and publishes merged snapshots.
//
// Agents report independently, so the store keeps each agent's last good data
// and rebuilds the snapshot whenever any of them reports.
type Store struct {
	// interner resolves label indices. Sorting and formatting need the text
	// behind a label, which the indices alone do not carry.
	interner *model.Interner

	mu     sync.Mutex
	agents map[string]*agentData

	current atomic.Pointer[Snapshot]

	// dirty is set when agent data changed and the published snapshot no
	// longer reflects it.
	dirty bool
	// rebuilding guards against several goroutines rebuilding at once.
	rebuilding bool
	// settled is closed and replaced each time a rebuild finishes, so Flush
	// can wait for one.
	settled chan struct{}

	// onPublish is called after each rebuild, for tests.
	onPublish func()
}

// New returns a store resolving labels through in. Passing nil creates a
// private interner, which suits tests that build targets themselves.
func New(in *model.Interner) *Store {
	if in == nil {
		in = model.NewInterner()
	}
	s := &Store{
		interner: in,
		agents:   make(map[string]*agentData),
		settled:  make(chan struct{}),
	}
	s.current.Store(&Snapshot{TakenAt: time.Now(), byAgent: map[string]int{}})
	return s
}

// Snapshot returns the current view. Safe for concurrent use and never nil.
func (s *Store) Snapshot() *Snapshot { return s.current.Load() }

// Interner returns the interner label indices resolve against.
func (s *Store) Interner() *model.Interner { return s.interner }

// Apply records one agent's poll result and republishes the snapshot.
//
// A failed poll updates the agent's status but keeps its previous targets, so
// an unreachable agent's view degrades to stale rather than vanishing.
func (s *Store) Apply(r poll.AgentResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name := r.Status.Name
	prev, existed := s.agents[name]
	data := &agentData{status: r.Status}

	if r.Result != nil {
		data.active = r.Result.Active
		data.dropped = r.Result.Dropped
	} else if existed {
		data.active = prev.active
		data.dropped = prev.dropped
		// Carry forward the last success so staleness is measured from the
		// most recent poll that actually returned data.
		data.status.LastSuccess = prev.status.LastSuccess
		data.status.ActiveTargets = prev.status.ActiveTargets
		data.status.DroppedTargets = prev.status.DroppedTargets
		data.status.UpTargets = prev.status.UpTargets
		data.status.DownTargets = prev.status.DownTargets
	}

	data.history = appendHistory(prev, r)
	s.agents[name] = data
	s.markDirty()
}

// appendHistory adds this poll's outcome and drops what has aged out.
func appendHistory(prev *agentData, r poll.AgentResult) []PollRecord {
	var h []PollRecord
	if prev != nil {
		h = prev.history
	}
	at := r.Status.LastPoll
	if at.IsZero() {
		at = time.Now()
	}
	h = append(h, PollRecord{At: at, OK: r.Status.Err == ""})

	cutoff := time.Now().Add(-HistoryWindow)
	first := 0
	for first < len(h) && h[first].At.Before(cutoff) {
		first++
	}
	if excess := len(h) - first - HistoryLimit; excess > 0 {
		first += excess
	}
	if first > 0 {
		h = append([]PollRecord(nil), h[first:]...)
	}
	return h
}

// RetainAgents drops any agent not in keep, so agents removed from the config
// stop appearing in the snapshot.
func (s *Store) RetainAgents(keep []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	wanted := make(map[string]struct{}, len(keep))
	for _, n := range keep {
		wanted[n] = struct{}{}
	}
	for name := range s.agents {
		if _, ok := wanted[name]; !ok {
			delete(s.agents, name)
		}
	}
	s.markDirty()
}

// markDirty records that agent data changed and starts a rebuild if one is
// not already running. Callers hold s.mu.
//
// Rebuilding is O(total targets), so with a fleet of agents reporting
// independently a rebuild per report would dominate: reports that arrive
// while one is in flight are absorbed into the next.
func (s *Store) markDirty() {
	s.dirty = true
	if s.rebuilding {
		return
	}
	s.rebuilding = true
	go s.rebuildLoop()
}

// rebuildLoop republishes until no further changes have arrived.
func (s *Store) rebuildLoop() {
	for {
		s.mu.Lock()
		if !s.dirty {
			s.rebuilding = false
			// Wake anything waiting for the store to settle.
			close(s.settled)
			s.settled = make(chan struct{})
			s.mu.Unlock()
			return
		}
		s.dirty = false
		s.publish()
		onPublish := s.onPublish
		s.mu.Unlock()

		if onPublish != nil {
			onPublish()
		}
	}
}

// Flush waits for pending changes to be published. Without it a reader has no
// way to know whether the newest report is visible yet.
func (s *Store) Flush() {
	for {
		s.mu.Lock()
		if !s.dirty && !s.rebuilding {
			s.mu.Unlock()
			return
		}
		wait := s.settled
		s.mu.Unlock()
		<-wait
	}
}

// publish rebuilds the snapshot from current agent data. Callers hold s.mu.
func (s *Store) publish() {
	snap := &Snapshot{
		TakenAt: time.Now(),
		Agents:  make([]model.AgentStatus, 0, len(s.agents)),
		byAgent: make(map[string]int, len(s.agents)),
		history: make(map[string][]PollRecord, len(s.agents)),
	}

	byKey := make(map[model.TargetKey]*Entry)
	pools := make(map[string]struct{})

	names := make([]string, 0, len(s.agents))
	for name := range s.agents {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		data := s.agents[name]
		snap.byAgent[name] = len(snap.Agents)
		snap.Agents = append(snap.Agents, data.status)
		snap.history[name] = data.history

		for _, list := range [][]model.Target{data.active, data.dropped} {
			for i := range list {
				tgt := &list[i]
				if tgt.ScrapePool != "" {
					pools[tgt.ScrapePool] = struct{}{}
				}
				merge(byKey, tgt, name)

				if tgt.Dropped {
					snap.DroppedCount++
					continue
				}
				snap.ActiveCount++
				if tgt.IsUp() {
					snap.UpCount++
				} else {
					snap.DownCount++
				}
			}
		}
	}

	snap.Targets = make([]*Entry, 0, len(byKey))
	for _, e := range byKey {
		snap.Targets = append(snap.Targets, e)
	}
	// Sorted by identity so snapshot order is stable between rebuilds and
	// pagination stays consistent across requests.
	slices.SortFunc(snap.Targets, func(a, b *Entry) int { return compareEntries(s.interner, a, b) })

	snap.ScrapePools = make([]string, 0, len(pools))
	for p := range pools {
		snap.ScrapePools = append(snap.ScrapePools, p)
	}
	sort.Strings(snap.ScrapePools)

	s.current.Store(snap)
}

// merge adds a target to the index, recording agent against an existing entry
// when the same physical target is already known.
func merge(byKey map[model.TargetKey]*Entry, tgt *model.Target, agent string) {
	key := tgt.Key()
	e, ok := byKey[key]
	if !ok {
		byKey[key] = &Entry{
			Target:        *tgt,
			Agents:        []string{agent},
			HealthByAgent: map[string]string{agent: tgt.Health},
		}
		return
	}
	// Agents are visited in sorted order, so appending keeps them sorted.
	if !slices.Contains(e.Agents, agent) {
		e.Agents = append(e.Agents, agent)
	}
	e.HealthByAgent[agent] = tgt.Health

	// Agents poll on independent schedules, so keep the freshest observation
	// of the target's scrape state.
	if tgt.LastScrape.After(e.LastScrape) {
		agents, health := e.Agents, e.HealthByAgent
		e.Target = *tgt
		e.Agents, e.HealthByAgent = agents, health
	}

	// A failure seen by any agent has to survive the merge: showing the
	// target as up because one agent reached it would hide the fault the
	// disagreement represents.
	if !tgt.IsUp() && e.IsUp() {
		e.Health = tgt.Health
		e.LastError = tgt.LastError
	}
}

// compareEntries orders targets by scrape URL, then by label content.
//
// Labels are compared pair by pair rather than by formatting each set into a
// string: the comparator runs O(n log n) times over the whole fleet, and
// building a string per call dominated the rebuild.
func compareEntries(in *model.Interner, a, b *Entry) int {
	if c := strings.Compare(a.ScrapeURL, b.ScrapeURL); c != 0 {
		return c
	}
	// Labels compare by text, not by index: indices reflect the order strings
	// were first seen, which would make ordering depend on poll timing.
	for i := 0; i < len(a.Labels) && i < len(b.Labels); i++ {
		if c := strings.Compare(in.Name(a.Labels[i]), in.Name(b.Labels[i])); c != 0 {
			return c
		}
		if c := strings.Compare(in.Value(a.Labels[i]), in.Value(b.Labels[i])); c != 0 {
			return c
		}
	}
	// One set is a prefix of the other, so the shorter sorts first.
	return len(a.Labels) - len(b.Labels)
}
