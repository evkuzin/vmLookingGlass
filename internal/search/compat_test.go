package search

import (
	"testing"

	"github.com/evkuzin/vmLookingGlass/internal/model"
	"github.com/evkuzin/vmLookingGlass/internal/store"
)

// The filter syntax is shared with an existing standalone tool, so these cases
// pin the semantics that tool established: "=" anchors, "=~" searches, "!~"
// holds when the label is absent, and health compares exactly.
//
// Expected values were produced by running the same filters through that
// tool's matcher.
func compatTargets() []*store.Entry {
	// The package interner: labels resolve only against the one that made
	// them, and matching uses this one.
	i := in
	mk := func(labels map[string]string, pool, health string, dur float64, samples int64) *store.Entry {
		return &store.Entry{
			Target: model.Target{
				ScrapePool:         pool,
				Labels:             i.Labels(labels),
				Health:             health,
				LastScrapeDuration: dur,
				LastSamplesScraped: samples,
			},
		}
	}
	return []*store.Entry{
		mk(map[string]string{"job": "node_exporter", "env": "prod"}, "consul-services", "up", 0.4, 500),
		mk(map[string]string{"job": "redis", "env": "dev"}, "redis-multi", "down", 1.5, 20),
		mk(map[string]string{"instance": "h1:9100"}, "blackbox_icmp", "up", 0.05, 0),
	}
}

func TestFilterCompatibility(t *testing.T) {
	cases := []struct {
		filter string
		want   [3]bool
	}{
		{"labels.job=node_exporter", [3]bool{true, false, false}},
		{"labels.job=node", [3]bool{false, false, false}},
		{"labels.job=node.*", [3]bool{true, false, false}},
		{"labels.job=~node", [3]bool{true, false, false}},
		{"labels.job=~exp", [3]bool{true, false, false}},
		{"labels.job!~redis", [3]bool{true, false, true}},
		{"labels.job!~node", [3]bool{false, true, true}},
		{"labels.env=prod", [3]bool{true, false, false}},
		{"labels.missing=x", [3]bool{false, false, false}},
		{"labels.missing=~x", [3]bool{false, false, false}},
		{"labels.missing!~x", [3]bool{true, true, true}},
		{"scrapePool=consul-services", [3]bool{true, false, false}},
		{"scrapePool=consul.*", [3]bool{true, false, false}},
		{"scrapePool=consul", [3]bool{false, false, false}},
		{"scrapePool=.*multi.*", [3]bool{false, true, false}},
		{"health=up", [3]bool{true, false, true}},
		{"health=down", [3]bool{false, true, false}},
		{"health=u.", [3]bool{false, false, false}},
		{"lastScrapeDuration>0.3", [3]bool{true, true, false}},
		{"lastScrapeDuration<0.3", [3]bool{false, false, true}},
		{"lastScrapeDuration>=0.4", [3]bool{true, true, false}},
		{"lastScrapeDuration<=0.4", [3]bool{true, false, true}},
		{"lastScrapeDuration==0.4", [3]bool{true, false, false}},
		{"lastScrapeDuration!=0.4", [3]bool{false, true, true}},
		{"lastScrapeDuration0.4", [3]bool{true, false, false}},
		{"lastSamplesScraped>100", [3]bool{true, false, false}},
		{"lastSamplesScraped<100", [3]bool{false, true, true}},
		{"lastSamplesScraped==500", [3]bool{true, false, false}},
		{"lastSamplesScraped!=500", [3]bool{false, true, true}},
		{"lastSamplesScraped0", [3]bool{false, false, true}},
	}

	targets := compatTargets()
	for _, tc := range cases {
		t.Run(tc.filter, func(t *testing.T) {
			q, err := Parse([]string{tc.filter})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			for i, want := range tc.want {
				if got := q.Match(in, targets[i]); got != want {
					t.Errorf("target %d: got %v, want %v", i, got, want)
				}
			}
		})
	}
}
