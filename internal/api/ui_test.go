package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/evkuzin/vmLookingGlass/internal/store"
)

// stubUI stands in for the embedded assets.
func stubUI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>ui</html>"))
	})
}

// API routes must win over the UI, or the UI's catch-all would swallow them.
func TestUIDoesNotShadowAPI(t *testing.T) {
	s := store.New(nil)
	h := New(s, discardLogger()).WithUI(stubUI()).Handler()

	for _, path := range []string{"/api/v1/targets", "/api/v1/agents", "/api/v1/scrape_pools", "/-/healthy"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
		}
		if body := rec.Body.String(); body == "<html>ui</html>" {
			t.Errorf("%s was served the UI instead of the API", path)
		}
	}
}

func TestUIServedAtRoot(t *testing.T) {
	s := store.New(nil)
	h := New(s, discardLogger()).WithUI(stubUI()).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "<html>ui</html>" {
		t.Errorf("root did not serve the UI: %q", rec.Body.String())
	}
}

// Without a UI the root must 404 rather than serving an empty page.
func TestRootWithoutUI(t *testing.T) {
	_, h := testServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when no UI is embedded", rec.Code)
	}
}

// An unknown API path must stay a 404 rather than falling through to the UI,
// so a mistyped endpoint is not answered with HTML.
func TestUnknownAPIPathNotServedByUI(t *testing.T) {
	s := store.New(nil)
	h := New(s, discardLogger()).WithUI(stubUI()).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nope", http.NoBody))
	if body := rec.Body.String(); body == "<html>ui</html>" {
		t.Error("unknown API path was answered with the UI")
	}
}
