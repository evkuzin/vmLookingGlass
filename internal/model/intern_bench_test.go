package model

import (
	"runtime"
	"testing"
)

// TestInternDedupRatio checks that interning collapses a fleet-shaped payload
// by a wide margin. A regression here means label memory grows with target
// count rather than with the size of the label vocabulary.
func TestInternDedupRatio(t *testing.T) {
	targets := generateTargets(1, defaultGenOptions())

	var rawBytes, pairs int
	count := func(m map[string]string) {
		for k, v := range m {
			rawBytes += len(k) + len(v)
			pairs++
		}
	}
	in := NewInterner()
	for _, tgt := range targets {
		count(tgt.DiscoveredLabels)
		count(tgt.Labels)
		in.Labels(tgt.DiscoveredLabels)
		in.Labels(tgt.Labels)
	}

	internedBytes := in.Bytes()

	ratio := float64(rawBytes) / float64(internedBytes)
	t.Logf("targets=%d pairs=%d unique=%d raw=%.1fMB interned=%.3fMB ratio=%.0fx",
		len(targets), pairs, in.Len(),
		float64(rawBytes)/1e6, float64(internedBytes)/1e6, ratio)

	if ratio < 20 {
		t.Errorf("dedup ratio %.0fx is far below expectation; interning may be broken", ratio)
	}
}

// BenchmarkInternPayload reports the per-agent cost of interning one poll,
// which is the figure that scales by agent count in the running daemon.
func BenchmarkInternPayload(b *testing.B) {
	targets := generateTargets(1, defaultGenOptions())

	b.ResetTimer()
	for b.Loop() {
		in := NewInterner()
		for _, tgt := range targets {
			in.Labels(tgt.DiscoveredLabels)
			in.Labels(tgt.Labels)
		}
		runtime.KeepAlive(in)
	}
}
