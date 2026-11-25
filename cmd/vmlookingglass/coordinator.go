package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/evkuzin/vmLookingGlass/internal/config"
	"github.com/evkuzin/vmLookingGlass/internal/netbox"
)

// refreshRecorder accounts for each refresh outcome in the metrics.
type refreshRecorder interface {
	RecordNetboxRefresh(netbox.Refresh)
}

// refresher is the discovery loop the coordinator starts and stops.
type refresher interface {
	Run(context.Context)
}

// refreshRun is one running refresher, identified by its generation so a result
// from a stopped one is ignored.
type refreshRun struct {
	gen    uint64
	cancel context.CancelFunc
	done   chan struct{}
}

func (r *refreshRun) stop() {
	r.cancel()
	<-r.done
}

// agentCoordinator merges config-file reloads with the latest discovered agent
// set and applies the result to the daemon. The two inputs arrive independently
// and concurrently, so the merge always uses the latest of each. It also owns
// the NetBox refresher, restarting it when the file's netbox block changes.
type agentCoordinator struct {
	mu    sync.Mutex
	file  *config.Config
	names []string
	// netbox is the block the running refresher was built from, or nil when
	// none is running.
	netbox       *config.Netbox
	run          *refreshRun
	gen          uint64
	newRefresher func(*config.Netbox, netbox.ReportFunc) (refresher, error)
	reload       func(*config.Config)
	metrics      refreshRecorder
	log          *slog.Logger
}

func newAgentCoordinator(file *config.Config, reload func(*config.Config), m refreshRecorder, log *slog.Logger) *agentCoordinator {
	return &agentCoordinator{
		file:    file,
		reload:  reload,
		metrics: m,
		log:     log,
		newRefresher: func(n *config.Netbox, report netbox.ReportFunc) (refresher, error) {
			r, err := netbox.NewRefresher(netbox.NewClient(n.URL), netbox.Options{
				Tags:     n.Tags,
				Exclude:  n.Exclude,
				Interval: n.Interval,
				Timeout:  n.Timeout,
			}, log, report)
			if err != nil {
				return nil, err
			}
			return r, nil
		},
	}
}

// merged returns the config for the daemon's initial load: the latest file
// config plus the latest discovered names.
func (c *agentCoordinator) merged() *config.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return config.MergeDiscovered(c.file, c.names)
}

// Start begins discovery if the initial config asks for it.
func (c *agentCoordinator) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file.Netbox == nil || c.run != nil {
		return
	}
	if c.start(c.file.Netbox) {
		c.log.Info("netbox discovery started", "tags", c.file.Netbox.Tags)
	}
}

// Stop cancels the running refresher and waits for it to return.
func (c *agentCoordinator) Stop() {
	c.mu.Lock()
	run := c.run
	c.run = nil
	c.netbox = nil
	c.gen++
	c.mu.Unlock()
	if run != nil {
		run.stop()
	}
}

// fileConfig records a newly loaded config file, reapplies the merge and moves
// discovery to whatever the new netbox block asks for.
func (c *agentCoordinator) fileConfig(file *config.Config) {
	c.mu.Lock()
	if c.netbox.Equal(file.Netbox) {
		c.file = file
		c.apply()
		c.mu.Unlock()
		return
	}

	// Detach before joining outside the lock: the refresher's report takes c.mu.
	old, was := c.run, c.netbox
	c.run, c.netbox = nil, nil
	c.gen++
	if file.Netbox == nil {
		c.names = nil
	}
	c.file = file
	c.mu.Unlock()

	if old != nil {
		old.stop()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case file.Netbox == nil:
		c.log.Info("netbox discovery stopped", "tags", was.Tags)
	case was == nil:
		if c.start(file.Netbox) {
			c.log.Info("netbox discovery started", "tags", file.Netbox.Tags)
		}
	default:
		// The old names stay in the merge until the first new fetch succeeds.
		if c.start(file.Netbox) {
			c.log.Info("netbox discovery restarted", "tags", file.Netbox.Tags)
		}
	}
	c.apply()
}

// start launches a refresher for n. Callers hold c.mu; the refresher's first
// fetch is immediate.
func (c *agentCoordinator) start(n *config.Netbox) bool {
	ctx, cancel := context.WithCancel(context.Background())
	run := &refreshRun{gen: c.gen, cancel: cancel, done: make(chan struct{})}
	r, err := c.newRefresher(n, func(res netbox.Refresh) { c.refresh(run.gen, res) })
	if err != nil {
		cancel()
		close(run.done)
		c.log.Error("netbox discovery not started", "err", err)
		return false
	}
	c.run = run
	c.netbox = n
	go func() {
		defer close(run.done)
		r.Run(ctx)
	}()
	return true
}

// refresh records a refresh outcome and, when it produced a set, reapplies the
// merge. Failed and rejected refreshes leave the current set in place, and a
// result from a stopped refresher is ignored entirely.
func (c *agentCoordinator) refresh(gen uint64, r netbox.Refresh) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	c.metrics.RecordNetboxRefresh(r)
	if r.Outcome != netbox.OutcomeOK {
		return
	}
	c.names = r.Names
	c.apply()
}

// apply merges and reloads. Callers hold c.mu.
func (c *agentCoordinator) apply() {
	merged := config.MergeDiscovered(c.file, c.names)
	c.logDropped(merged)
	c.reload(merged)
}

// logDropped names the discovered agents that a static entry already covers.
func (c *agentCoordinator) logDropped(merged *config.Config) {
	kept := make(map[string]struct{}, len(merged.Agents))
	for _, a := range merged.Agents {
		kept[a.Name] = struct{}{}
	}
	for _, name := range c.names {
		if _, ok := kept[name]; !ok {
			c.log.Debug("discovered agent skipped, already configured", "name", name)
		}
	}
}
