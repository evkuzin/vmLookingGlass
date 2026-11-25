// Package metrics exposes vmLookingGlass's own state for scraping.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/evkuzin/vmLookingGlass/internal/model"
	"github.com/evkuzin/vmLookingGlass/internal/netbox"
	"github.com/evkuzin/vmLookingGlass/internal/poll"
	"github.com/evkuzin/vmLookingGlass/internal/store"
)

const namespace = "vmlookingglass"

// Metrics holds the counters this process owns and the collector that derives
// gauges from the current snapshot.
type Metrics struct {
	registry *prometheus.Registry

	polls        *prometheus.CounterVec
	pollDuration *prometheus.HistogramVec

	netboxRefreshes   *prometheus.CounterVec
	netboxDevices     prometheus.Gauge
	netboxLastSuccess prometheus.Gauge
}

// New registers all metrics against reg.
//
// Counters accumulate as polls complete; every gauge is read from the current
// snapshot at scrape time, so an agent removed from the config stops being
// exported instead of leaving a stale series behind.
func New(reg *prometheus.Registry, s *store.Store, in *model.Interner) *Metrics {
	m := &Metrics{
		registry: reg,
		polls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "agent_polls_total",
			Help:      "Polls of each agent by outcome.",
		}, []string{"agent", "result"}),
		pollDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "agent_poll_duration_seconds",
			Help:      "Time to fetch and decode one agent's targets.",
			// A poll spans a fast local agent to a slow cross-region one, so
			// the buckets run from milliseconds to tens of seconds.
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"agent"}),
		netboxRefreshes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "netbox_refreshes_total",
			Help:      "NetBox inventory refreshes by outcome.",
		}, []string{"result"}),
		netboxDevices: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "netbox_devices",
			Help:      "Devices in the current discovered agent set.",
		}),
		netboxLastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "netbox_last_success_timestamp_seconds",
			Help:      "Unix time of the last successful NetBox refresh, 0 until one succeeds.",
		}),
	}

	reg.MustRegister(m.polls, m.pollDuration)
	reg.MustRegister(m.netboxRefreshes, m.netboxDevices, m.netboxLastSuccess)
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(&snapshotCollector{store: s, interner: in})
	return m
}

// Handler serves the exposition endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// RecordPoll accounts for one completed poll.
func (m *Metrics) RecordPoll(r poll.AgentResult) {
	result := "success"
	if r.Status.Err != "" {
		result = "error"
	}
	m.polls.WithLabelValues(r.Status.Name, result).Inc()
	m.pollDuration.WithLabelValues(r.Status.Name).Observe(r.Status.Duration.Seconds())
}

// RecordNetboxRefresh accounts for one inventory refresh. The current-set
// gauges only move on a success; a failed or rejected refresh leaves them at
// the set still in use.
func (m *Metrics) RecordNetboxRefresh(r netbox.Refresh) {
	m.netboxRefreshes.WithLabelValues(string(r.Outcome)).Inc()
	if r.Outcome != netbox.OutcomeOK {
		return
	}
	m.netboxDevices.Set(float64(len(r.Names)))
	m.netboxLastSuccess.SetToCurrentTime()
}

func desc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(namespace+"_"+name, help, labels, nil)
}

var (
	agentUp        = desc("agent_up", "Whether the agent's last poll succeeded.", "agent")
	agentStale     = desc("agent_stale", "Whether the agent's data is older than its poll interval allows.", "agent")
	agentActive    = desc("agent_active_targets", "Active targets reported by the agent.", "agent")
	agentDropped   = desc("agent_dropped_targets", "Dropped targets reported by the agent.", "agent")
	agentUpTargets = desc("agent_up_targets", "Active targets the agent scraped successfully.", "agent")
	agentDownTgts  = desc("agent_down_targets", "Active targets whose last scrape failed.", "agent")
	agentTruncated = desc("agent_dropped_truncated",
		"Whether the agent's dropped list reached the maximum it is configured "+
			"to report, meaning entries were evicted and the list is partial.", "agent")

	totalAgents   = desc("agents", "Configured agents.")
	totalTargets  = desc("targets", "Distinct targets across the fleet, merged across agents.")
	multiScraped  = desc("multi_scraped_targets", "Targets scraped by more than one agent.")
	activeTargets = desc("active_targets", "Active targets across the fleet, merged across agents.")
	droppedTgts   = desc("dropped_targets", "Dropped targets across the fleet, merged across agents.")
	internedStr   = desc("interned_strings", "Distinct label strings held by the interner.")
	snapshotAge   = desc("snapshot_age_seconds", "Time since the snapshot was last rebuilt.")
)

// snapshotCollector derives gauges from the current snapshot at scrape time,
// so the exported series always match the configured agents.
type snapshotCollector struct {
	store    *store.Store
	interner *model.Interner
}

func (c *snapshotCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		agentUp, agentStale, agentActive, agentDropped, agentUpTargets, agentDownTgts,
		agentTruncated, totalAgents, totalTargets, multiScraped, activeTargets,
		droppedTgts, internedStr, snapshotAge,
	} {
		ch <- d
	}
}

func (c *snapshotCollector) Collect(ch chan<- prometheus.Metric) {
	snap := c.store.Snapshot()
	now := snap.TakenAt

	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}

	for i := range snap.Agents {
		a := &snap.Agents[i]
		gauge(agentUp, boolValue(a.Healthy()), a.Name)
		gauge(agentStale, boolValue(a.IsStale(now)), a.Name)
		gauge(agentActive, float64(a.ActiveTargets), a.Name)
		gauge(agentDropped, float64(a.DroppedTargets), a.Name)
		gauge(agentUpTargets, float64(a.UpTargets), a.Name)
		gauge(agentDownTgts, float64(a.DownTargets), a.Name)
		gauge(agentTruncated, boolValue(a.DroppedTruncated), a.Name)
	}

	var multi int
	for _, t := range snap.Targets {
		if t.MultiScraped() {
			multi++
		}
	}

	gauge(totalAgents, float64(len(snap.Agents)))
	gauge(totalTargets, float64(len(snap.Targets)))
	gauge(multiScraped, float64(multi))
	gauge(activeTargets, float64(snap.ActiveCount))
	gauge(droppedTgts, float64(snap.DroppedCount))
	gauge(internedStr, float64(c.interner.Len()))
	gauge(snapshotAge, time.Since(snap.TakenAt).Seconds())
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
