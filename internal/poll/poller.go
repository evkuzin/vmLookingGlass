package poll

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/config"
	"github.com/evkuzin/vmLookingGlass/internal/model"
)

// targetsPath is the vmagent endpoint. state=any returns dropped targets
// alongside active ones, which are needed to debug relabel rules.
const targetsPath = "/api/v1/targets?state=any"

// maxIdleConnsPerHost keeps a connection open per agent between polls, since
// each agent is polled repeatedly on its own schedule.
const maxIdleConnsPerHost = 2

// Poller fetches target status from a single agent.
type Poller struct {
	client *http.Client
	log    *slog.Logger

	// droppedCaps holds each agent's dropped-target cap, read from its flags
	// once. The cap only changes when an agent restarts with new arguments,
	// so refetching it every poll would be wasted work.
	mu          sync.Mutex
	droppedCaps map[string]int
}

func New(log *slog.Logger) *Poller {
	return &Poller{
		log:         log,
		droppedCaps: make(map[string]int),
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        256,
				MaxIdleConnsPerHost: maxIdleConnsPerHost,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// droppedCap returns the agent's dropped-target cap, reading it once per agent.
func (p *Poller) droppedCap(ctx context.Context, a config.Agent) int {
	p.mu.Lock()
	n, ok := p.droppedCaps[a.Name]
	p.mu.Unlock()
	if ok {
		return n
	}

	n = p.fetchMaxDroppedTargets(ctx, a)

	p.mu.Lock()
	p.droppedCaps[a.Name] = n
	p.mu.Unlock()
	return n
}

// AgentResult pairs an agent's decoded targets with the outcome of the poll.
// Result is nil when the poll failed.
type AgentResult struct {
	Result *Result
	Status model.AgentStatus
}

// pollAgent fetches and decodes one agent's targets. The returned status is
// always populated; the result is nil on failure.
func (p *Poller) pollAgent(ctx context.Context, a config.Agent, in *model.Interner) (*Result, model.AgentStatus) {
	status := model.AgentStatus{
		Name:     a.Name,
		URL:      a.URL,
		Interval: a.Interval,
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout)
	defer cancel()

	start := time.Now()
	res, err := p.fetch(ctx, a, in)
	status.Duration = time.Since(start)
	status.LastPoll = time.Now()

	if err != nil {
		status.Err = err.Error()
		return nil, status
	}

	status.LastSuccess = status.LastPoll
	status.ActiveTargets = len(res.Active)
	status.DroppedTargets = len(res.Dropped)

	// The targets API reports neither the cap nor the true dropped total, so
	// a list that reaches the agent's configured maximum is the only sign
	// that entries were evicted.
	status.MaxDroppedTargets = p.droppedCap(ctx, a)
	status.DroppedTruncated = status.DroppedTargets >= status.MaxDroppedTargets

	for i := range res.Active {
		if res.Active[i].IsUp() {
			status.UpTargets++
		} else {
			status.DownTargets++
		}
	}
	return res, status
}

func (p *Poller) fetch(ctx context.Context, a config.Agent, in *model.Interner) (*Result, error) {
	// The agent URL is normalized at config load to scheme://host:port with no
	// path or query, so the endpoint appends cleanly.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL+targetsPath, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %s", resp.Status)
	}
	return decodeTargets(resp.Body, a.Name, in)
}

// PublishFunc receives each poll result as it completes.
type PublishFunc func(AgentResult)

// Runner polls every configured agent on its own schedule.
//
// One goroutine per agent, each with its own ticker, so agents with different
// intervals are independent and a stalled agent delays only itself.
type Runner struct {
	// Jitter is the fraction of an agent's interval used to spread the initial
	// poll, so agents do not all fire at the same instant.
	Jitter float64

	poller  *Poller
	publish PublishFunc
	log     *slog.Logger

	// interner is shared by all agents so labels common across the fleet are
	// stored once.
	interner *model.Interner
}

// defaultJitter spreads initial polls across a fraction of each interval.
const defaultJitter = 0.1

func NewRunner(log *slog.Logger, publish PublishFunc) *Runner {
	return &Runner{
		Jitter:   defaultJitter,
		poller:   New(log),
		publish:  publish,
		log:      log,
		interner: model.NewInterner(),
	}
}

// Interner returns the shared label interner.
func (r *Runner) Interner() *model.Interner { return r.interner }

// SetInterner replaces the interner, so several runners can share one label
// vocabulary. It must be called before Run.
func (r *Runner) SetInterner(in *model.Interner) { r.interner = in }

// Run starts one goroutine per agent and blocks until ctx is canceled.
func (r *Runner) Run(ctx context.Context, agents []config.Agent) {
	var wg sync.WaitGroup
	for _, a := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.runAgent(ctx, a)
		}()
	}
	wg.Wait()
}

// runAgent polls one agent until ctx is canceled. Failures are reported and
// retried on the next tick rather than ending the loop.
func (r *Runner) runAgent(ctx context.Context, a config.Agent) {
	if d := r.startDelay(a.Interval); d > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}

	ticker := time.NewTicker(a.Interval)
	defer ticker.Stop()

	for {
		r.pollOnce(ctx, a)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) pollOnce(ctx context.Context, a config.Agent) {
	res, status := r.poller.pollAgent(ctx, a, r.interner)
	// A canceled context means shutdown, not an agent fault; the partial
	// result would otherwise be published as a spurious failure.
	if ctx.Err() != nil {
		return
	}
	if status.Err != "" {
		r.log.Warn("poll failed", "agent", a.Name, "err", status.Err)
	}
	r.publish(AgentResult{Result: res, Status: status})
}

// startDelay returns a random offset within the jitter fraction of interval.
func (r *Runner) startDelay(interval time.Duration) time.Duration {
	if r.Jitter <= 0 {
		return 0
	}
	span := float64(interval) * r.Jitter
	if span <= 0 {
		return 0
	}
	return time.Duration(rand.Float64() * span)
}
