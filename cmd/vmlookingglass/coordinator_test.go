package main

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"

	"github.com/evkuzin/vmLookingGlass/internal/config"
	"github.com/evkuzin/vmLookingGlass/internal/netbox"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type recorder struct {
	got []netbox.Refresh
}

func (r *recorder) RecordNetboxRefresh(res netbox.Refresh) { r.got = append(r.got, res) }

// fakeRefresher stands in for a discovery loop until its context is canceled.
type fakeRefresher struct {
	report netbox.ReportFunc
	done   chan struct{}
}

func (f *fakeRefresher) Run(ctx context.Context) {
	<-ctx.Done()
	close(f.done)
}

// refresherFactory hands out fake refreshers and records them in order.
type refresherFactory struct {
	mu   sync.Mutex
	made []*fakeRefresher
}

func (f *refresherFactory) new(n *config.Netbox, report netbox.ReportFunc) (refresher, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &fakeRefresher{report: report, done: make(chan struct{})}
	f.made = append(f.made, r)
	return r, nil
}

func (f *refresherFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.made)
}

func (f *refresherFactory) at(t *testing.T, i int) *fakeRefresher {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.made) {
		t.Fatalf("refresher %d not created, have %d", i, len(f.made))
	}
	return f.made[i]
}

// newTestCoordinator builds a coordinator whose refreshers are fakes.
func newTestCoordinator(t *testing.T, file *config.Config, reload func(*config.Config), m refreshRecorder) (*agentCoordinator, *refresherFactory) {
	t.Helper()
	c := newAgentCoordinator(file, reload, m, discardLogger())
	f := &refresherFactory{}
	c.newRefresher = f.new
	t.Cleanup(c.Stop)
	return c, f
}

func parseConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func agentNames(c *config.Config) []string {
	names := make([]string, len(c.Agents))
	for i, a := range c.Agents {
		names[i] = a.Name
	}
	return names
}

const (
	fileWithStatic1 = `
netbox:
  tags: [a]
agents:
  - url: static1
`
	fileWithStatic2 = `
netbox:
  tags: [a]
agents:
  - url: static2
`
	fileNetboxB = `
netbox:
  tags: [b]
agents:
  - url: static1
`
	fileNoNetbox = `
agents:
  - url: static1
`
)

func TestCoordinatorFileReloadKeepsDiscovered(t *testing.T) {
	var got []*config.Config
	c, _ := newTestCoordinator(t, parseConfig(t, fileWithStatic1),
		func(c *config.Config) { got = append(got, c) }, &recorder{})
	c.Start()

	c.refresh(c.gen, netbox.Refresh{Outcome: netbox.OutcomeOK, Names: []string{"d1"}})
	c.fileConfig(parseConfig(t, fileWithStatic2))

	if len(got) != 2 {
		t.Fatalf("got %d reloads, want 2", len(got))
	}
	if want := []string{"static2", "d1"}; !reflect.DeepEqual(agentNames(got[1]), want) {
		t.Errorf("agents after the file reload = %v, want %v", agentNames(got[1]), want)
	}
}

func TestCoordinatorDiscoveryUsesLatestFileConfig(t *testing.T) {
	var got []*config.Config
	c, _ := newTestCoordinator(t, parseConfig(t, fileWithStatic1),
		func(c *config.Config) { got = append(got, c) }, &recorder{})
	c.Start()

	c.fileConfig(parseConfig(t, fileWithStatic2))
	c.refresh(c.gen, netbox.Refresh{Outcome: netbox.OutcomeOK, Names: []string{"d1"}})

	if len(got) != 2 {
		t.Fatalf("got %d reloads, want 2", len(got))
	}
	if want := []string{"static2", "d1"}; !reflect.DeepEqual(agentNames(got[1]), want) {
		t.Errorf("agents after the refresh = %v, want %v", agentNames(got[1]), want)
	}
}

// A failed refresh must not trigger a reload, or the daemon would churn its
// agents against an unchanged set.
func TestCoordinatorFailureDoesNotReload(t *testing.T) {
	var got []*config.Config
	rec := &recorder{}
	c, _ := newTestCoordinator(t, parseConfig(t, fileWithStatic1),
		func(c *config.Config) { got = append(got, c) }, rec)
	c.Start()

	c.refresh(c.gen, netbox.Refresh{Outcome: netbox.OutcomeError})

	if len(got) != 0 {
		t.Errorf("got %d reloads after a failed refresh, want 0", len(got))
	}
	if len(rec.got) != 1 {
		t.Errorf("recorded %d outcomes, want 1", len(rec.got))
	}
}

func TestCoordinatorStartsDiscoveryFromInitialConfig(t *testing.T) {
	c, f := newTestCoordinator(t, parseConfig(t, fileWithStatic1),
		func(*config.Config) {}, &recorder{})

	c.Start()
	if n := f.count(); n != 1 {
		t.Fatalf("created %d refreshers, want 1", n)
	}

	c.Stop()
	select {
	case <-f.at(t, 0).done:
	default:
		t.Error("Stop returned before the refresher did")
	}
}

func TestCoordinatorRestartsDiscoveryOnBlockChange(t *testing.T) {
	var got []*config.Config
	rec := &recorder{}
	c, f := newTestCoordinator(t, parseConfig(t, fileWithStatic1),
		func(c *config.Config) { got = append(got, c) }, rec)
	c.Start()
	first := f.at(t, 0)
	first.report(netbox.Refresh{Outcome: netbox.OutcomeOK, Names: []string{"d1"}})

	c.fileConfig(parseConfig(t, fileNetboxB))

	if n := f.count(); n != 2 {
		t.Fatalf("created %d refreshers, want 2", n)
	}
	select {
	case <-first.done:
	default:
		t.Fatal("old refresher still running after a block change")
	}
	second := f.at(t, 1)
	if want := []string{"static1", "d1"}; !reflect.DeepEqual(agentNames(got[len(got)-1]), want) {
		t.Errorf("agents during the swap = %v, want %v", agentNames(got[len(got)-1]), want)
	}

	first.report(netbox.Refresh{Outcome: netbox.OutcomeOK, Names: []string{"stale"}})
	second.report(netbox.Refresh{Outcome: netbox.OutcomeOK, Names: []string{"d2"}})
	if want := []string{"static1", "d2"}; !reflect.DeepEqual(agentNames(got[len(got)-1]), want) {
		t.Errorf("agents after the new refresh = %v, want %v", agentNames(got[len(got)-1]), want)
	}
	if len(rec.got) != 2 {
		t.Errorf("recorded %d refreshes, want 2 with the stale one skipped", len(rec.got))
	}
}

func TestCoordinatorStartsDiscoveryOnFileConfigBeforeStart(t *testing.T) {
	var got []*config.Config
	c, f := newTestCoordinator(t, parseConfig(t, fileNoNetbox),
		func(c *config.Config) { got = append(got, c) }, &recorder{})

	c.fileConfig(parseConfig(t, fileWithStatic1))
	c.Start()

	if n := f.count(); n != 1 {
		t.Fatalf("created %d refreshers, want 1", n)
	}
	f.at(t, 0).report(netbox.Refresh{Outcome: netbox.OutcomeOK, Names: []string{"d1"}})
	if want := []string{"static1", "d1"}; !reflect.DeepEqual(agentNames(got[len(got)-1]), want) {
		t.Errorf("agents after the refresh = %v, want %v", agentNames(got[len(got)-1]), want)
	}
}

func TestCoordinatorStopsDiscoveryWhenBlockRemoved(t *testing.T) {
	var got []*config.Config
	c, f := newTestCoordinator(t, parseConfig(t, fileWithStatic1),
		func(c *config.Config) { got = append(got, c) }, &recorder{})
	c.Start()
	f.at(t, 0).report(netbox.Refresh{Outcome: netbox.OutcomeOK, Names: []string{"d1"}})

	c.fileConfig(parseConfig(t, fileNoNetbox))

	if n := f.count(); n != 1 {
		t.Errorf("created %d refreshers, want no new one", n)
	}
	if want := []string{"static1"}; !reflect.DeepEqual(agentNames(got[len(got)-1]), want) {
		t.Errorf("agents after removing the block = %v, want %v", agentNames(got[len(got)-1]), want)
	}
	select {
	case <-f.at(t, 0).done:
	default:
		t.Error("refresher still running after the block was removed")
	}
}

func TestCoordinatorStartsDiscoveryWhenBlockAdded(t *testing.T) {
	var got []*config.Config
	c, f := newTestCoordinator(t, parseConfig(t, fileNoNetbox),
		func(c *config.Config) { got = append(got, c) }, &recorder{})

	c.fileConfig(parseConfig(t, fileWithStatic1))
	if n := f.count(); n != 1 {
		t.Fatalf("created %d refreshers, want 1", n)
	}
	f.at(t, 0).report(netbox.Refresh{Outcome: netbox.OutcomeOK, Names: []string{"d1"}})

	if want := []string{"static1", "d1"}; !reflect.DeepEqual(agentNames(got[len(got)-1]), want) {
		t.Errorf("agents after adding the block = %v, want %v", agentNames(got[len(got)-1]), want)
	}
}

func TestCoordinatorKeepsDiscoveryWhenBlockUnchanged(t *testing.T) {
	var got []*config.Config
	c, f := newTestCoordinator(t, parseConfig(t, fileWithStatic1),
		func(c *config.Config) { got = append(got, c) }, &recorder{})
	c.Start()
	first := f.at(t, 0)

	c.fileConfig(parseConfig(t, fileWithStatic1))

	if n := f.count(); n != 1 {
		t.Errorf("created %d refreshers, want 1", n)
	}
	select {
	case <-first.done:
		t.Error("unchanged block stopped the refresher")
	default:
	}
	if len(got) != 1 {
		t.Errorf("got %d reloads, want 1", len(got))
	}
}
