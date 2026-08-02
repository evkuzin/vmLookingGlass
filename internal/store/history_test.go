package store

import (
	"testing"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/model"
	"github.com/evkuzin/vmLookingGlass/internal/poll"
)

func pollAt(name string, ts time.Time, ok bool) poll.AgentResult {
	st := model.AgentStatus{Name: name, LastPoll: ts, Interval: time.Minute}
	if ok {
		st.LastSuccess = ts
		return poll.AgentResult{Result: &poll.Result{}, Status: st}
	}
	st.Err = "connection refused"
	return poll.AgentResult{Status: st}
}

func TestHistoryRecordsOutcomes(t *testing.T) {
	s := New(nil)
	now := time.Now()
	s.Apply(pollAt("a1", now.Add(-3*time.Minute), true))
	s.Apply(pollAt("a1", now.Add(-2*time.Minute), false))
	s.Apply(pollAt("a1", now.Add(-1*time.Minute), true))
	s.Flush()

	h := s.Snapshot().History("a1")
	if len(h) != 3 {
		t.Fatalf("history = %d entries, want 3", len(h))
	}
	if !h[0].OK || h[1].OK || !h[2].OK {
		t.Errorf("outcomes = %v %v %v, want true false true", h[0].OK, h[1].OK, h[2].OK)
	}
	// Oldest first, so the bar reads left to right as time passing.
	if !h[0].At.Before(h[2].At) {
		t.Error("history is not in chronological order")
	}
}

// Only the recent window is kept: the bar covers a fixed span and unbounded
// history would grow without limit.
func TestHistoryDropsEntriesOutsideWindow(t *testing.T) {
	s := New(nil)
	now := time.Now()
	s.Apply(pollAt("a1", now.Add(-13*time.Hour), true))
	s.Apply(pollAt("a1", now.Add(-11*time.Hour), true))
	s.Apply(pollAt("a1", now, true))
	s.Flush()

	h := s.Snapshot().History("a1")
	for _, e := range h {
		if time.Since(e.At) > HistoryWindow {
			t.Errorf("entry from %v is outside the %v window", e.At, HistoryWindow)
		}
	}
	if len(h) != 2 {
		t.Errorf("history = %d entries, want the 2 inside the window", len(h))
	}
}

// A busy fleet polls often, so the per-agent history has to stay bounded even
// within the window.
func TestHistoryIsBounded(t *testing.T) {
	s := New(nil)
	now := time.Now()
	for i := range HistoryLimit * 2 {
		s.Apply(pollAt("a1", now.Add(-time.Duration(HistoryLimit*2-i)*time.Second), true))
	}
	s.Flush()

	if got := len(s.Snapshot().History("a1")); got > HistoryLimit {
		t.Errorf("history = %d entries, want at most %d", got, HistoryLimit)
	}
}

func TestHistoryPerAgent(t *testing.T) {
	s := New(nil)
	now := time.Now()
	s.Apply(pollAt("a1", now, true))
	s.Apply(pollAt("a2", now, false))
	s.Flush()

	snap := s.Snapshot()
	if h := snap.History("a1"); len(h) != 1 || !h[0].OK {
		t.Errorf("a1 history = %v", h)
	}
	if h := snap.History("a2"); len(h) != 1 || h[0].OK {
		t.Errorf("a2 history = %v", h)
	}
	if h := snap.History("nosuch"); h != nil {
		t.Errorf("unknown agent returned %v, want nil", h)
	}
}

// An agent removed from the config must not keep its history around.
func TestHistoryDroppedWithAgent(t *testing.T) {
	s := New(nil)
	s.Apply(pollAt("a1", time.Now(), true))
	s.Apply(pollAt("a2", time.Now(), true))
	s.Flush()

	s.RetainAgents([]string{"a1"})
	s.Flush()

	if h := s.Snapshot().History("a2"); h != nil {
		t.Errorf("removed agent kept %d history entries", len(h))
	}
}
