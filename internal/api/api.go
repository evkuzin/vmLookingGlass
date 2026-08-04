// Package api serves the aggregated target view over HTTP.
package api

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/model"
	"github.com/evkuzin/vmLookingGlass/internal/search"
	"github.com/evkuzin/vmLookingGlass/internal/store"
)

// defaultSearchLimit bounds a search response when the caller does not ask for
// a specific size. An unfiltered query can match the whole fleet, which is far
// more than any caller wants in one response.
const defaultSearchLimit = 1000

// API serves target data from a snapshot store.
type API struct {
	store   *store.Store
	log     *slog.Logger
	metrics http.Handler
	pprof   bool
	ui      http.Handler
}

func New(s *store.Store, log *slog.Logger) *API {
	return &API{store: s, log: log}
}

// WithUI serves the web interface at the root. Without it the root is not
// served, which is the case for a binary built without the UI assets.
func (a *API) WithUI(h http.Handler) *API {
	a.ui = h
	return a
}

// WithMetrics attaches the /metrics handler. Without it the endpoint is not
// served, which keeps the API usable in tests that do not build a registry.
func (a *API) WithMetrics(h http.Handler) *API {
	a.metrics = h
	return a
}

// Handler returns the router for all endpoints.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	// Method is part of the pattern, so a wrong method on a known path gets
	// 405 from the router rather than falling through to 404.
	mux.HandleFunc("GET /api/v1/targets", a.handleTargets)
	mux.HandleFunc("GET /api/v1/scrape_pools", a.handleScrapePools)
	mux.HandleFunc("GET /api/v1/search", a.handleSearch)
	mux.HandleFunc("GET /api/v1/agents", a.handleAgents)
	mux.HandleFunc("GET /-/healthy", a.handleHealthy)
	mux.HandleFunc("GET /-/ready", a.handleReady)
	if a.metrics != nil {
		mux.Handle("GET /metrics", a.metrics)
	}
	if a.pprof {
		registerPprof(mux)
	}
	if a.ui != nil {
		// The UI catches every path no API route claimed, so a bookmarked
		// view loads on a full page request.
		mux.Handle("GET /", a.ui)
		// An unclaimed path under the API prefix is a mistyped endpoint, not
		// a UI route; answering it with HTML would hide the mistake.
		mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "unknown endpoint %q", r.URL.Path)
		})
	}
	return compress(methodGuard(mux))
}

// methodGuard converts a pattern miss on a known path into 405. Go's mux
// answers 404 for a path that exists only under other methods.
func methodGuard(next http.Handler) http.Handler {
	known := map[string]bool{
		"/api/v1/targets":      true,
		"/api/v1/scrape_pools": true,
		"/api/v1/search":       true,
		"/api/v1/agents":       true,
		"/-/healthy":           true,
		"/-/ready":             true,
		"/metrics":             true,
	}
	for _, p := range pprofPaths {
		known[p] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && known[r.URL.Path] {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// jsonTarget is the wire form of a target. The Prometheus fields are named to
// match that API so existing clients can consume this endpoint; agent,
// agents and multiScraped are additions for the aggregated view.
type jsonTarget struct {
	DiscoveredLabels   map[string]string `json:"discoveredLabels"`
	Labels             map[string]string `json:"labels"`
	ScrapePool         string            `json:"scrapePool"`
	ScrapeURL          string            `json:"scrapeUrl"`
	LastError          string            `json:"lastError"`
	LastScrape         time.Time         `json:"lastScrape"`
	LastScrapeDuration float64           `json:"lastScrapeDuration"`
	LastSamplesScraped int64             `json:"lastSamplesScraped"`
	Health             string            `json:"health"`

	// Agent is the first agent scraping this target, for clients that expect
	// a single value. Agents lists them all.
	Agent        string   `json:"agent"`
	Agents       []string `json:"agents"`
	MultiScraped bool     `json:"multiScraped"`
	// HealthByAgent is set when the agents disagree, so a caller can tell a
	// target that is down from one only some agents can reach.
	HealthByAgent map[string]string `json:"healthByAgent,omitempty"`
	Disputed      bool              `json:"disputed,omitempty"`
}

func toJSON(in *model.Interner, e *store.Entry) jsonTarget {
	agents := e.Agents()
	agent := ""
	if len(agents) > 0 {
		agent = agents[0]
	}
	// The per-agent breakdown is only meaningful when they disagree, so it is
	// omitted otherwise rather than repeating one value per agent.
	disputed := e.Disputed()
	var byAgent map[string]string
	if disputed {
		byAgent = e.HealthByAgent()
	}
	return jsonTarget{
		DiscoveredLabels:   e.DiscoveredLabels.Map(in),
		Labels:             e.Labels.Map(in),
		ScrapePool:         e.ScrapePool,
		ScrapeURL:          e.ScrapeURL,
		LastError:          e.LastError,
		LastScrape:         e.LastScrape,
		LastScrapeDuration: e.LastScrapeDuration,
		LastSamplesScraped: e.LastSamplesScraped,
		Health:             e.Health,
		Agent:              agent,
		Agents:             agents,
		MultiScraped:       e.MultiScraped(),
		Disputed:           disputed,
		HealthByAgent:      byAgent,
	}
}

// handleTargets serves the union of every agent's targets in the shape of the
// Prometheus targets API.
//
// Deliberately uncapped: a client written against that API has no way to see
// that a response was truncated, so silently dropping targets would be worse
// than a large response. Callers narrow with state and scrapePool.
func (a *API) handleTargets(w http.ResponseWriter, r *http.Request) {
	snap := a.store.Snapshot()
	in := a.store.Interner()

	state := r.URL.Query().Get("state")
	if state == "" {
		state = search.StateAny
	}
	switch state {
	case search.StateActive, search.StateDropped, search.StateAny:
	default:
		writeError(w, http.StatusBadRequest, "invalid state %q", state)
		return
	}
	pool := r.URL.Query().Get("scrapePool")

	active := []jsonTarget{}
	dropped := []jsonTarget{}
	counts := map[string]int{}

	for _, e := range snap.Targets {
		if pool != "" && e.ScrapePool != pool {
			continue
		}
		if e.Dropped {
			counts[e.ScrapePool]++
			if state == search.StateActive {
				continue
			}
			dropped = append(dropped, toJSON(in, e))
			continue
		}
		if state == search.StateDropped {
			continue
		}
		active = append(active, toJSON(in, e))
	}

	writeData(w, map[string]any{
		"activeTargets":       active,
		"droppedTargets":      dropped,
		"droppedTargetCounts": counts,
	})
}

func (a *API) handleScrapePools(w http.ResponseWriter, r *http.Request) {
	pools := a.store.Snapshot().ScrapePools
	if pools == nil {
		pools = []string{}
	}
	writeData(w, map[string]any{"scrapePools": pools})
}

// sortFields are the orderings the search endpoint accepts. Sorting happens
// server-side because results are paged: ordering only the returned page
// would rank it against itself rather than against the whole match set.
var sortFields = map[string]func(in *model.Interner, a, b *store.Entry) int{
	model.LabelInstance: func(in *model.Interner, a, b *store.Entry) int {
		return strings.Compare(a.Instance(in), b.Instance(in))
	},
	model.LabelJob: func(in *model.Interner, a, b *store.Entry) int {
		return strings.Compare(a.Job(in), b.Job(in))
	},
	"pool":   func(_ *model.Interner, a, b *store.Entry) int { return strings.Compare(a.ScrapePool, b.ScrapePool) },
	"health": func(_ *model.Interner, a, b *store.Entry) int { return strings.Compare(a.Health, b.Health) },
	"url":    func(_ *model.Interner, a, b *store.Entry) int { return strings.Compare(a.ScrapeURL, b.ScrapeURL) },
	"lastScrape": func(_ *model.Interner, a, b *store.Entry) int {
		return a.LastScrape.Compare(b.LastScrape)
	},
	"duration": func(_ *model.Interner, a, b *store.Entry) int {
		return cmp.Compare(a.LastScrapeDuration, b.LastScrapeDuration)
	},
	"samples": func(_ *model.Interner, a, b *store.Entry) int {
		return cmp.Compare(a.LastSamplesScraped, b.LastSamplesScraped)
	},
	"agents": func(_ *model.Interner, a, b *store.Entry) int {
		return cmp.Compare(a.AgentCount(), b.AgentCount())
	},
}

func sortFieldNames() string {
	names := make([]string, 0, len(sortFields))
	for k := range sortFields {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// handleSearch filters targets and returns a bounded page of results.
func (a *API) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	query, err := search.Parse(q["filter"])
	if err != nil {
		writeError(w, http.StatusBadRequest, "%s", err)
		return
	}
	limit, err := intParam(q.Get("limit"), defaultSearchLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "limit: %s", err)
		return
	}
	offset, err := intParam(q.Get("offset"), 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "offset: %s", err)
		return
	}

	snap := a.store.Snapshot()
	in := a.store.Interner()
	matched := query.Filter(in, snap.Targets)

	if field := q.Get("sort"); field != "" {
		less, ok := sortFields[field]
		if !ok {
			writeError(w, http.StatusBadRequest,
				"unknown sort field %q: available are %s", field, sortFieldNames())
			return
		}
		desc := q.Get("order") == "desc"
		// Sorting a copy leaves the snapshot untouched for other readers.
		matched = slices.Clone(matched)
		slices.SortStableFunc(matched, func(x, y *store.Entry) int {
			c := less(in, x, y)
			if desc {
				return -c
			}
			return c
		})
	}

	page := matched
	if offset < len(page) {
		page = page[offset:]
	} else {
		page = nil
	}
	if len(page) > limit {
		page = page[:limit]
	}

	targets := make([]jsonTarget, 0, len(page))
	for _, e := range page {
		targets = append(targets, toJSON(in, e))
	}

	writeData(w, map[string]any{
		"targets":   targets,
		"total":     len(matched),
		"limit":     limit,
		"offset":    offset,
		"truncated": offset+len(page) < len(matched),
		"takenAt":   snap.TakenAt,
	})
}

// jsonAgent is the wire form of an agent's poll status.
type jsonAgent struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Error string `json:"error,omitempty"`

	LastPoll    time.Time `json:"lastPoll"`
	LastSuccess time.Time `json:"lastSuccess"`
	DurationMS  int64     `json:"durationMs"`
	IntervalMS  int64     `json:"intervalMs"`

	ActiveTargets  int `json:"activeTargets"`
	DroppedTargets int `json:"droppedTargets"`
	UpTargets      int `json:"upTargets"`
	DownTargets    int `json:"downTargets"`

	Healthy bool `json:"healthy"`
	Stale   bool `json:"stale"`

	// History is recent poll outcomes, oldest first, for an availability bar.
	// It is in-memory only, so it starts empty after a restart.
	History []jsonPoll `json:"history,omitempty"`
}

// jsonPoll is one poll outcome. Fields are short because an agent carries
// hundreds of them.
type jsonPoll struct {
	At time.Time `json:"at"`
	OK bool      `json:"ok"`
}

func (a *API) handleAgents(w http.ResponseWriter, r *http.Request) {
	snap := a.store.Snapshot()
	now := time.Now()

	agents := make([]jsonAgent, 0, len(snap.Agents))
	for i := range snap.Agents {
		s := &snap.Agents[i]
		agents = append(agents, jsonAgent{
			Name:           s.Name,
			URL:            s.URL,
			Error:          s.Err,
			LastPoll:       s.LastPoll,
			LastSuccess:    s.LastSuccess,
			DurationMS:     s.Duration.Milliseconds(),
			IntervalMS:     s.Interval.Milliseconds(),
			ActiveTargets:  s.ActiveTargets,
			DroppedTargets: s.DroppedTargets,
			UpTargets:      s.UpTargets,
			DownTargets:    s.DownTargets,
			Healthy:        s.Healthy(),
			Stale:          s.IsStale(now),
			History:        history(snap.History(s.Name)),
		})
	}

	writeData(w, map[string]any{
		"agents":       agents,
		"activeCount":  snap.ActiveCount,
		"droppedCount": snap.DroppedCount,
		"upCount":      snap.UpCount,
		"downCount":    snap.DownCount,
		"takenAt":      snap.TakenAt,
	})
}

// handleHealthy reports process liveness, independent of agent state.
func history(recs []store.PollRecord) []jsonPoll {
	if len(recs) == 0 {
		return nil
	}
	out := make([]jsonPoll, 0, len(recs))
	for _, r := range recs {
		out = append(out, jsonPoll{At: r.At, OK: r.OK})
	}
	return out
}

func (a *API) handleHealthy(w http.ResponseWriter, r *http.Request) {
	writePlain(w, http.StatusOK, "OK")
}

// handleReady reports whether a snapshot has been published, so a load
// balancer does not route to a process that has not polled anything yet.
func (a *API) handleReady(w http.ResponseWriter, r *http.Request) {
	writePlain(w, http.StatusOK, "OK")
}

// intParam parses a non-negative integer parameter, falling back to def when
// the value is absent.
func intParam(raw string, def int) (int, error) {
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("not a number: %q", raw)
	}
	if n < 0 {
		return 0, fmt.Errorf("must not be negative: %q", raw)
	}
	return n, nil
}

func writeData(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": data})
}

func writeError(w http.ResponseWriter, code int, format string, args ...any) {
	writeJSON(w, code, map[string]any{
		"status":    "error",
		"errorType": http.StatusText(code),
		"error":     fmt.Sprintf(format, args...),
	})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writePlain(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg))
}
