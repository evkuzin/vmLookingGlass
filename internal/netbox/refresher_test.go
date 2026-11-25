package netbox

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// devicesServer serves a device page whose contents and health the test can
// change between refreshes.
type devicesServer struct {
	*httptest.Server

	mu    sync.Mutex
	names []string
	fail  bool
}

func newDevicesServer(t *testing.T, names []string) *devicesServer {
	t.Helper()
	ds := &devicesServer{names: names}
	ds.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ds.mu.Lock()
		names, fail := ds.names, ds.fail
		ds.mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writePage(w, names, "")
	}))
	t.Cleanup(ds.Close)
	return ds
}

func (d *devicesServer) set(names []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.names = names
}

func (d *devicesServer) setFail(fail bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fail = fail
}

func newTestRefresher(t *testing.T, srv *devicesServer, opts Options, report ReportFunc) *Refresher {
	t.Helper()
	if opts.Tags == nil {
		opts.Tags = []string{"t"}
	}
	if opts.Interval == 0 {
		opts.Interval = time.Hour
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	r, err := NewRefresher(NewClient(srv.URL), opts, discardLogger(), report)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}
	return r
}

func hostNames(n int) []string {
	names := make([]string, n)
	for i := range n {
		names[i] = fmt.Sprintf("host-%03d", i)
	}
	return names
}

func awaitOutcome(t *testing.T, ch chan Refresh) Refresh {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a refresh outcome")
		return Refresh{}
	}
}

func TestRefreshFiltersNameShapeAndExclude(t *testing.T) {
	srv := newDevicesServer(t, []string{"good-host", "Bad_Host", "test-host", "vmti1", "other"})
	ch := make(chan Refresh, 4)
	r := newTestRefresher(t, srv, Options{Exclude: "test|vmti"}, func(res Refresh) { ch <- res })

	r.refresh(context.Background())
	res := awaitOutcome(t, ch)

	if res.Outcome != OutcomeOK {
		t.Fatalf("outcome = %s, want ok (err %v)", res.Outcome, res.Err)
	}
	if want := []string{"good-host", "other"}; !reflect.DeepEqual(res.Names, want) {
		t.Errorf("names = %v, want %v", res.Names, want)
	}
	if res.Dropped != 3 {
		t.Errorf("dropped = %d, want 3", res.Dropped)
	}
	if want := []string{"good-host", "other"}; !reflect.DeepEqual(r.Names(), want) {
		t.Errorf("current set = %v, want %v", r.Names(), want)
	}
}

func TestRefreshFailureKeepsPreviousSet(t *testing.T) {
	srv := newDevicesServer(t, []string{"a", "b"})
	ch := make(chan Refresh, 4)
	r := newTestRefresher(t, srv, Options{}, func(res Refresh) { ch <- res })

	r.refresh(context.Background())
	awaitOutcome(t, ch)

	srv.setFail(true)
	r.refresh(context.Background())
	res := awaitOutcome(t, ch)

	if res.Outcome != OutcomeError {
		t.Errorf("outcome = %s, want error", res.Outcome)
	}
	if res.Err == nil {
		t.Error("error outcome has no error")
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(r.Names(), want) {
		t.Errorf("current set = %v, want the previous %v", r.Names(), want)
	}
}

func TestRefreshZeroDevicesKeepsPreviousSet(t *testing.T) {
	srv := newDevicesServer(t, []string{"a"})
	ch := make(chan Refresh, 4)
	r := newTestRefresher(t, srv, Options{}, func(res Refresh) { ch <- res })

	r.refresh(context.Background())
	awaitOutcome(t, ch)

	srv.set(nil)
	r.refresh(context.Background())
	res := awaitOutcome(t, ch)

	if res.Outcome != OutcomeError {
		t.Errorf("outcome = %s, want error for an empty inventory", res.Outcome)
	}
	if want := []string{"a"}; !reflect.DeepEqual(r.Names(), want) {
		t.Errorf("current set = %v, want the previous %v", r.Names(), want)
	}
}

func TestRefreshShrinkGuard(t *testing.T) {
	srv := newDevicesServer(t, hostNames(100))
	ch := make(chan Refresh, 4)
	r := newTestRefresher(t, srv, Options{}, func(res Refresh) { ch <- res })

	r.refresh(context.Background())
	if res := awaitOutcome(t, ch); res.Outcome != OutcomeOK {
		t.Fatalf("first outcome = %s, want ok", res.Outcome)
	}

	srv.set(hostNames(40))
	r.refresh(context.Background())
	res := awaitOutcome(t, ch)
	if res.Outcome != OutcomeRejected {
		t.Errorf("outcome for 100 -> 40 = %s, want rejected", res.Outcome)
	}
	if res.Previous != 100 || res.Next != 40 {
		t.Errorf("rejection counts = %d/%d, want 100/40", res.Previous, res.Next)
	}
	if got := len(r.Names()); got != 100 {
		t.Errorf("current set has %d names, want the previous 100", got)
	}

	srv.set(hostNames(60))
	r.refresh(context.Background())
	if res := awaitOutcome(t, ch); res.Outcome != OutcomeOK {
		t.Errorf("outcome for 100 -> 60 = %s, want ok", res.Outcome)
	}
	if got := len(r.Names()); got != 60 {
		t.Errorf("current set has %d names, want 60", got)
	}
}

func TestRefreshShrinkGuardExactlyHalf(t *testing.T) {
	srv := newDevicesServer(t, hostNames(100))
	ch := make(chan Refresh, 4)
	r := newTestRefresher(t, srv, Options{}, func(res Refresh) { ch <- res })

	r.refresh(context.Background())
	if res := awaitOutcome(t, ch); res.Outcome != OutcomeOK {
		t.Fatalf("first outcome = %s, want ok", res.Outcome)
	}

	srv.set(hostNames(50))
	r.refresh(context.Background())
	res := awaitOutcome(t, ch)
	if res.Outcome != OutcomeOK {
		t.Errorf("outcome for 100 -> 50 = %s, want ok", res.Outcome)
	}
	if want := hostNames(50); !reflect.DeepEqual(r.Names(), want) {
		t.Errorf("current set has %d names, want the new 50", len(r.Names()))
	}
}

func TestFirstSuccessNeverRejected(t *testing.T) {
	srv := newDevicesServer(t, hostNames(1))
	ch := make(chan Refresh, 4)
	r := newTestRefresher(t, srv, Options{}, func(res Refresh) { ch <- res })

	r.refresh(context.Background())
	if res := awaitOutcome(t, ch); res.Outcome != OutcomeOK {
		t.Errorf("first outcome = %s, want ok", res.Outcome)
	}
}

func TestRunRefreshesImmediatelyAndStops(t *testing.T) {
	srv := newDevicesServer(t, []string{"a", "b"})
	ch := make(chan Refresh, 4)
	r := newTestRefresher(t, srv, Options{Interval: time.Hour}, func(res Refresh) { ch <- res })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	if res := awaitOutcome(t, ch); res.Outcome != OutcomeOK {
		t.Errorf("first outcome = %s, want ok", res.Outcome)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestNewRefresherValidation(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{"no tags", Options{Interval: time.Minute, Timeout: time.Second}},
		{"zero interval", Options{Tags: []string{"a"}, Timeout: time.Second}},
		{"zero timeout", Options{Tags: []string{"a"}, Interval: time.Minute}},
		{"bad exclude", Options{Tags: []string{"a"}, Interval: time.Minute, Timeout: time.Second, Exclude: "("}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRefresher(NewClient("http://example"), tt.opts, discardLogger(), func(Refresh) {}); err == nil {
				t.Error("expected an error")
			}
		})
	}
}
