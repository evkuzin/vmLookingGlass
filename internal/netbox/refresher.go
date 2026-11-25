package netbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

// deviceName is the shape of a name that can become an agent host.
var deviceName = regexp.MustCompile(`^[a-z0-9-]+$`)

// Outcome classifies one refresh attempt.
type Outcome string

const (
	// OutcomeOK is a refresh that produced a new current set.
	OutcomeOK Outcome = "ok"
	// OutcomeError is a refresh that failed; the current set is unchanged.
	OutcomeError Outcome = "error"
	// OutcomeRejected is a set that shrank too far to be believed.
	OutcomeRejected Outcome = "rejected"
)

// Refresh is the result of one refresh attempt. Names is set only for OutcomeOK.
type Refresh struct {
	Outcome  Outcome
	Names    []string
	Dropped  int
	Duration time.Duration
	Err      error
	Previous int
	Next     int
}

// ReportFunc receives every refresh outcome.
type ReportFunc func(Refresh)

// Options configure a Refresher.
type Options struct {
	// Tags are queried separately and unioned.
	Tags []string
	// Exclude drops a device whose name matches. Empty means no exclusion.
	Exclude string
	// Interval is the delay between refreshes.
	Interval time.Duration
	// Timeout bounds one whole refresh.
	Timeout time.Duration
}

// Refresher queries the inventory on a schedule and keeps the current set.
type Refresher struct {
	client   *Client
	tags     []string
	exclude  *regexp.Regexp
	interval time.Duration
	timeout  time.Duration
	log      *slog.Logger
	report   ReportFunc

	mu      sync.Mutex
	current []string
	// known distinguishes the first success, which the shrink guard must not
	// reject, from a later one.
	known bool
}

// NewRefresher returns a Refresher that queries client every opts.Interval.
func NewRefresher(client *Client, opts Options, log *slog.Logger, report ReportFunc) (*Refresher, error) {
	if len(opts.Tags) == 0 {
		return nil, errors.New("netbox: tags is required")
	}
	if opts.Interval <= 0 {
		return nil, errors.New("netbox: interval must be positive")
	}
	if opts.Timeout <= 0 {
		return nil, errors.New("netbox: timeout must be positive")
	}
	var exclude *regexp.Regexp
	if opts.Exclude != "" {
		re, err := regexp.Compile(opts.Exclude)
		if err != nil {
			return nil, fmt.Errorf("netbox: exclude: %w", err)
		}
		exclude = re
	}
	return &Refresher{
		client:   client,
		tags:     opts.Tags,
		exclude:  exclude,
		interval: opts.Interval,
		timeout:  opts.Timeout,
		log:      log,
		report:   report,
	}, nil
}

// Names returns a copy of the current set.
func (r *Refresher) Names() []string {
	names, _ := r.state()
	return names
}

func (r *Refresher) state() ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.current...), r.known
}

// Run refreshes immediately, then every interval, until ctx is canceled.
func (r *Refresher) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		r.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// refresh performs one attempt and reports its outcome.
func (r *Refresher) refresh(parent context.Context) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()

	names, err := r.client.Devices(ctx, r.tags)
	// A canceled parent means shutdown, not a failed inventory; the attempt is
	// abandoned rather than reported against the previous set.
	if parent.Err() != nil {
		return
	}

	previous, known := r.state()
	res := Refresh{Duration: time.Since(start), Previous: len(previous)}
	switch {
	case err != nil:
		res.Outcome = OutcomeError
		res.Err = err
		r.log.Error("netbox refresh failed", "err", err)
	default:
		next, dropped := r.keep(names)
		res.Dropped = dropped
		res.Next = len(next)
		switch {
		case len(next) == 0:
			res.Outcome = OutcomeError
			res.Err = errors.New("no devices found")
			r.log.Error("netbox refresh failed", "err", res.Err)
		case known && 2*len(next) < len(previous):
			res.Outcome = OutcomeRejected
			r.log.Error("netbox refresh rejected", "previous", len(previous), "next", len(next))
		default:
			res.Outcome = OutcomeOK
			res.Names = append([]string(nil), next...)
			r.accept(next)
			r.log.Info("netbox refresh", "devices", len(next), "dropped", dropped,
				"duration", res.Duration)
		}
	}
	r.report(res)
}

// keep applies the name shape and exclude rules, returning the survivors in
// their original order.
func (r *Refresher) keep(names []string) ([]string, int) {
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if !deviceName.MatchString(name) {
			continue
		}
		if r.exclude != nil && r.exclude.MatchString(name) {
			continue
		}
		kept = append(kept, name)
	}
	return kept, len(names) - len(kept)
}

func (r *Refresher) accept(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = names
	r.known = true
}
