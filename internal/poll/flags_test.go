package poll

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/model"
)

func TestParseFlagsExplicitValue(t *testing.T) {
	body := `-promscrape.config="/etc/vmagent/scrape.yml"
-promscrape.maxDroppedTargets="50000"
-remoteWrite.url="secret"
`
	got := parseMaxDroppedTargets(strings.NewReader(body))
	if got != 50000 {
		t.Errorf("maxDroppedTargets = %d, want 50000", got)
	}
}

// flag.Visit only reports flags that were set, so an agent running with the
// default does not list it at all.
func TestParseFlagsAbsentMeansDefault(t *testing.T) {
	body := `-promscrape.config="/etc/vmagent/scrape.yml"
-httpListenAddr=":8429"
`
	if got := parseMaxDroppedTargets(strings.NewReader(body)); got != DefaultMaxDroppedTargets {
		t.Errorf("maxDroppedTargets = %d, want the default %d", got, DefaultMaxDroppedTargets)
	}
}

func TestParseFlagsMalformedValue(t *testing.T) {
	tests := []string{
		`-promscrape.maxDroppedTargets="notanumber"`,
		`-promscrape.maxDroppedTargets=`,
		`-promscrape.maxDroppedTargets="-5"`,
		``,
	}
	for _, body := range tests {
		if got := parseMaxDroppedTargets(strings.NewReader(body)); got != DefaultMaxDroppedTargets {
			t.Errorf("%q: got %d, want the default %d", body, got, DefaultMaxDroppedTargets)
		}
	}
}

// A flag whose name merely contains the one we want must not be mistaken
// for it.
func TestParseFlagsSimilarName(t *testing.T) {
	body := `-promscrape.maxDroppedTargetsFoo="1"
-notpromscrape.maxDroppedTargets="2"
`
	if got := parseMaxDroppedTargets(strings.NewReader(body)); got != DefaultMaxDroppedTargets {
		t.Errorf("matched a similarly named flag: got %d", got)
	}
}

func TestFetchFlagsFromAgent(t *testing.T) {
	srv := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/flags" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "-promscrape.maxDroppedTargets=\"25000\"\n")
	})

	p := New(discardLogger())
	got := p.fetchMaxDroppedTargets(context.Background(), agentCfg("a1", srv.URL))
	if got != 25000 {
		t.Errorf("maxDroppedTargets = %d, want 25000", got)
	}
}

// An agent that does not serve /flags, or serves it behind an auth key, must
// fall back to the default rather than failing the poll.
func TestFetchFlagsUnavailable(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"not found", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}},
		{"unauthorized", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "auth key required", http.StatusUnauthorized)
		}},
		{"empty", func(w http.ResponseWriter, r *http.Request) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newAgentServer(t, tt.handler)
			p := New(discardLogger())
			got := p.fetchMaxDroppedTargets(context.Background(), agentCfg("a1", srv.URL))
			if got != DefaultMaxDroppedTargets {
				t.Errorf("got %d, want the default %d", got, DefaultMaxDroppedTargets)
			}
		})
	}
}

func TestFetchFlagsUnreachable(t *testing.T) {
	p := New(discardLogger())
	cfg := agentCfg("dead", "http://127.0.0.1:1")
	cfg.Timeout = 300 * time.Millisecond

	if got := p.fetchMaxDroppedTargets(context.Background(), cfg); got != DefaultMaxDroppedTargets {
		t.Errorf("got %d, want the default %d", got, DefaultMaxDroppedTargets)
	}
}

// The poll reports the cap so truncation can be judged against the agent's
// own configuration rather than an assumed default.
func TestPollReportsDroppedCap(t *testing.T) {
	srv := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/flags" {
			fmt.Fprint(w, "-promscrape.maxDroppedTargets=\"3\"\n")
			return
		}
		fmt.Fprint(w, targetsBody(1))
	})

	p := New(discardLogger())
	_, status := p.pollAgent(context.Background(), agentCfg("a1", srv.URL), newInternerForTest())
	if status.MaxDroppedTargets != 3 {
		t.Errorf("MaxDroppedTargets = %d, want 3", status.MaxDroppedTargets)
	}
}

// Truncation is judged against whatever the agent actually runs with.
func TestDroppedTruncatedUsesAgentCap(t *testing.T) {
	var dropped []string
	for i := range 3 {
		dropped = append(dropped, fmt.Sprintf(
			`{"discoveredLabels":{"__address__":"h%d:9119","job":"p"}}`, i))
	}
	srv := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/flags" {
			fmt.Fprint(w, "-promscrape.maxDroppedTargets=\"3\"\n")
			return
		}
		fmt.Fprintf(w, `{"status":"success","data":{"activeTargets":[],"droppedTargets":[%s]}}`,
			strings.Join(dropped, ","))
	})

	p := New(discardLogger())
	_, status := p.pollAgent(context.Background(), agentCfg("a1", srv.URL), newInternerForTest())
	if !status.DroppedTruncated {
		t.Errorf("3 dropped against a cap of 3 not flagged truncated")
	}
}

func TestDroppedNotTruncatedBelowCap(t *testing.T) {
	srv := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/flags" {
			fmt.Fprint(w, "-promscrape.maxDroppedTargets=\"100\"\n")
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"activeTargets":[],"droppedTargets":[
		  {"discoveredLabels":{"__address__":"h:9119","job":"p"}}]}}`)
	})

	p := New(discardLogger())
	_, status := p.pollAgent(context.Background(), agentCfg("a1", srv.URL), newInternerForTest())
	if status.DroppedTruncated {
		t.Error("1 dropped against a cap of 100 wrongly flagged truncated")
	}
}

// The cap rarely changes, so it is not refetched on every poll.
func TestFlagsFetchedOnce(t *testing.T) {
	var flagHits int
	srv := newAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/flags" {
			flagHits++
			fmt.Fprint(w, "-promscrape.maxDroppedTargets=\"7\"\n")
			return
		}
		fmt.Fprint(w, targetsBody(1))
	})

	p := New(discardLogger())
	cfg := agentCfg("a1", srv.URL)
	for range 3 {
		p.pollAgent(context.Background(), cfg, newInternerForTest())
	}
	if flagHits != 1 {
		t.Errorf("/flags fetched %d times across 3 polls, want 1", flagHits)
	}
}

func newInternerForTest() *model.Interner { return model.NewInterner() }
