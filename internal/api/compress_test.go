package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getRaw(t *testing.T, h http.Handler, path, encoding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	if encoding != "" {
		req.Header.Set("Accept-Encoding", encoding)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Target listings are label-heavy JSON that compresses many times over, and
// a fleet-wide response runs to hundreds of megabytes.
func TestResponsesAreCompressed(t *testing.T) {
	_, h := testServer(t)

	for _, path := range []string{"/api/v1/targets", "/api/v1/agents", "/api/v1/search?filter=health%3Dup"} {
		rec := getRaw(t, h, path, "gzip")
		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Errorf("%s: Content-Encoding = %q, want gzip", path, got)
		}
		// The body has to be real gzip, not just a header.
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("%s: body is not gzip: %v", path, err)
		}
		var body map[string]any
		if err := json.NewDecoder(zr).Decode(&body); err != nil {
			t.Errorf("%s: decompressed body is not JSON: %v", path, err)
		}
		if body["status"] != "success" {
			t.Errorf("%s: status = %v", path, body["status"])
		}
	}
}

// A client that does not ask for compression must still get a usable body.
func TestUncompressedWhenNotRequested(t *testing.T) {
	_, h := testServer(t)

	rec := getRaw(t, h, "/api/v1/agents", "")
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not plain JSON: %v", err)
	}
	if body["status"] != "success" {
		t.Errorf("status = %v", body["status"])
	}
}

// Only gzip is offered, so a client asking for something else gets plain text
// rather than a body it cannot read.
func TestUnsupportedEncodingServedPlain(t *testing.T) {
	_, h := testServer(t)

	rec := getRaw(t, h, "/api/v1/agents", "br")
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none for an unsupported encoding", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Errorf("body is not plain JSON: %v", err)
	}
}

// gzip listed among several encodings still counts.
func TestGzipAmongSeveralEncodings(t *testing.T) {
	_, h := testServer(t)

	rec := getRaw(t, h, "/api/v1/agents", "br, gzip;q=0.9, deflate")
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", got)
	}
}

// An error response must stay readable: compressing it correctly matters more
// than the few bytes saved, since this is what a caller debugs against.
func TestErrorResponseCompressed(t *testing.T) {
	_, h := testServer(t)

	rec := getRaw(t, h, "/api/v1/search?filter=bogus%3Dx", "gzip")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	body := rec.Body.Bytes()
	if rec.Header().Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("error body is not gzip: %v", err)
		}
		body, _ = io.ReadAll(zr)
	}
	if !strings.Contains(string(body), "unknown filter") {
		t.Errorf("error message lost: %s", body)
	}
}

// The UI is served by a file server, which must also compress: the bundle is
// hundreds of kilobytes of JavaScript.
func TestUIIsCompressed(t *testing.T) {
	s, _ := testServer(t)
	h := New(s, discardLogger()).WithUI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(strings.Repeat("<div>ui</div>", 500)))
	})).Handler()

	rec := getRaw(t, h, "/", "gzip")
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", got)
	}
}

// Compression must not alter the payload: a client that decompresses has to
// see exactly what an uncompressed client would.
func TestCompressionIsLossless(t *testing.T) {
	_, h := testServer(t)

	plain := getRaw(t, h, "/api/v1/targets", "").Body.Bytes()

	rec := getRaw(t, h, "/api/v1/targets", "gzip")
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("decompressed body differs from the plain one\n got %d bytes\nwant %d bytes",
			len(got), len(plain))
	}
}

// A cache must not hand a compressed body to a client that cannot read it.
func TestVaryHeaderSet(t *testing.T) {
	_, h := testServer(t)

	rec := getRaw(t, h, "/api/v1/targets", "gzip")
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q, want it to include Accept-Encoding", got)
	}
}

// These go through a real server rather than httptest.NewRecorder: a
// recorder does not enforce the transport rules that make these mistakes
// visible, so both faults pass unnoticed against it.
func TestCompressionOverRealConnection(t *testing.T) {
	_, h := testServer(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// A client that does not offer gzip must receive a body it can read,
	// with no encoding applied.
	t.Run("not offered", func(t *testing.T) {
		// The transport adds Accept-Encoding unless it is set, so an explicit
		// identity is the only way to ask for no compression.
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/targets", http.NoBody)
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if enc := resp.Header.Get("Content-Encoding"); enc != "" {
			t.Errorf("Content-Encoding = %q for a client that did not offer gzip", enc)
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("body unreadable: %v", err)
		}
		if body["status"] != "success" {
			t.Errorf("status = %v", body["status"])
		}
	})

	// A Content-Length describing the uncompressed body would truncate the
	// compressed one, which the transport reports as an unexpected EOF.
	t.Run("length matches the compressed body", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/targets", http.NoBody)
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}
		if cl := resp.ContentLength; cl >= 0 && cl != int64(len(raw)) {
			t.Errorf("Content-Length = %d but body is %d bytes", cl, len(raw))
		}
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("body is not gzip: %v", err)
		}
		if _, err := io.ReadAll(zr); err != nil {
			t.Errorf("truncated gzip stream: %v", err)
		}
	})
}

// The metrics handler compresses its own output. Wrapping it again would
// nest two gzip streams under a single Content-Encoding, which no client can
// read, and stripping its header would leave a compressed body unlabelled.
func TestHandlerOwnEncodingLeftAlone(t *testing.T) {
	s, _ := testServer(t)
	// Small enough to stay under the compression threshold, which is where
	// the header was previously being removed.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		defer zw.Close()
		_, _ = zw.Write([]byte("metric_value 1\n"))
	})
	h := New(s, discardLogger()).WithMetrics(inner).Handler()

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Values("Content-Encoding"); len(got) != 1 || got[0] != "gzip" {
		t.Fatalf("Content-Encoding = %v, want exactly one gzip", got)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(body) != "metric_value 1\n" {
		t.Errorf("body = %q; a second gzip layer would leave binary here", body)
	}
}
