// Package web serves the built UI from the binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The UI is built by `npm run build` in web/ui. The all: prefix keeps files
// whose names begin with an underscore, which asset hashing can produce.
//
//go:embed all:ui/dist
var assets embed.FS

// Available reports whether a built UI is embedded. The placeholder committed
// for builds made without running the UI build has no index.html.
func Available() bool {
	_, err := fs.Stat(assets, "ui/dist/index.html")
	return err == nil
}

// Handler serves the UI, falling back to index.html so client-side routes
// resolve on a full page load.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "ui/dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			// Unknown path: hand it to the app rather than 404ing, so a
			// bookmarked view loads.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
