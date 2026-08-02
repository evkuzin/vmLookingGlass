// Package daemon wires the poller, store and config reload together.
package daemon

import (
	"context"
	"log/slog"
	"sync"

	"github.com/evkuzin/vmLookingGlass/internal/config"
	"github.com/evkuzin/vmLookingGlass/internal/model"
	"github.com/evkuzin/vmLookingGlass/internal/poll"
	"github.com/evkuzin/vmLookingGlass/internal/store"
)

// Daemon polls every configured agent and keeps the aggregated store current.
//
// Agents run as independent goroutines, so a config reload starts and stops
// only the agents that actually changed and leaves the rest polling.
type Daemon struct {
	log      *slog.Logger
	store    *store.Store
	poller   *poll.Poller
	interner *model.Interner

	mu      sync.Mutex
	running map[string]*agentTask
	ctx     context.Context
	wg      sync.WaitGroup
}

// agentTask is one running agent goroutine and the config it was started with.
type agentTask struct {
	cfg    config.Agent
	cancel context.CancelFunc
	done   chan struct{}
}

func New(log *slog.Logger) *Daemon {
	return &Daemon{
		log:      log,
		store:    store.New(),
		poller:   poll.New(log),
		interner: model.NewInterner(),
		running:  make(map[string]*agentTask),
	}
}

// Store returns the aggregated snapshot store.
func (d *Daemon) Store() *store.Store { return d.store }

// Run starts polling and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context, c *config.Config) {
	d.mu.Lock()
	d.ctx = ctx
	d.mu.Unlock()

	d.Reload(c)

	<-ctx.Done()
	d.stopAll()
}

// Reload applies a new config, starting agents that appeared, stopping those
// that went away, and restarting only those whose settings changed.
func (d *Daemon) Reload(c *config.Config) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.ctx == nil || d.ctx.Err() != nil {
		return
	}

	wanted := make(map[string]config.Agent, len(c.Agents))
	for _, a := range c.Agents {
		wanted[a.Name] = a
	}

	for name, task := range d.running {
		next, keep := wanted[name]
		if keep && next.Equal(task.cfg) {
			continue
		}
		// Dropped from the config, or its settings changed: kill the goroutine
		// and wait for it, so a removed agent stops polling before Reload
		// returns.
		task.cancel()
		<-task.done
		delete(d.running, name)
	}

	for name, a := range wanted {
		if _, ok := d.running[name]; ok {
			continue
		}
		d.start(a)
	}

	names := make([]string, 0, len(wanted))
	for name := range wanted {
		names = append(names, name)
	}
	d.store.RetainAgents(names)

	d.log.Info("config applied", "agents", len(wanted))
}

// start launches one agent's poll loop. Callers hold d.mu.
func (d *Daemon) start(a config.Agent) {
	ctx, cancel := context.WithCancel(d.ctx)
	task := &agentTask{cfg: a, cancel: cancel, done: make(chan struct{})}
	d.running[a.Name] = task

	runner := poll.NewRunner(d.log, d.store.Apply)
	runner.SetInterner(d.interner)

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer close(task.done)
		runner.Run(ctx, []config.Agent{a})
	}()
}

// stopAll waits for every agent goroutine to exit so Run does not return while
// polls are still in flight. Cancelling Run's context already signals them,
// since each agent context derives from it.
func (d *Daemon) stopAll() {
	d.mu.Lock()
	clear(d.running)
	d.mu.Unlock()

	// Released the lock first: an exiting agent may still publish a result,
	// which takes the store lock, and holding both invites a deadlock.
	d.wg.Wait()
}
