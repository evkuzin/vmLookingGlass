package api

import (
	"net/http"
	"net/http/pprof"
)

// pprofPaths are served only when profiling is enabled. They expose runtime
// internals and can be expensive to collect, so they stay off by default.
var pprofPaths = []string{
	"/debug/pprof/",
	"/debug/pprof/cmdline",
	"/debug/pprof/profile",
	"/debug/pprof/symbol",
	"/debug/pprof/trace",
}

// WithPprof enables the runtime profiling endpoints.
func (a *API) WithPprof() *API {
	a.pprof = true
	return a
}

func registerPprof(mux *http.ServeMux) {
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
}
