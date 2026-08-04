package api

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// minCompressSize is the body size below which compression is skipped. JSON
// compresses well even at a few hundred bytes, so the threshold only has to
// clear gzip's own framing, which is about 20 bytes plus a small dictionary.
const minCompressSize = 256

// gzipWriters are pooled because a compressor allocates a sizeable window and
// a busy aggregator serves many responses.
var gzipWriters = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
		return w
	},
}

// compress gzips responses for clients that accept it.
//
// Target listings are label-heavy JSON that compresses many times over, and a
// fleet-wide listing runs to hundreds of megabytes, so serving it uncompressed
// wastes bandwidth on every request.
func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		// Responses vary by Accept-Encoding, so a cache must not serve a
		// compressed body to a client that cannot read it.
		w.Header().Add("Vary", "Accept-Encoding")

		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.Close()
		next.ServeHTTP(gw, r)
	})
}

func acceptsGzip(r *http.Request) bool {
	for enc := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		// Encodings may carry a quality value, as in "gzip;q=0.9".
		name, _, _ := strings.Cut(strings.TrimSpace(enc), ";")
		if name == "gzip" {
			return true
		}
	}
	return false
}

// gzipResponseWriter compresses the body once it is clear the response is
// large enough to be worth it.
//
// The decision needs the body, which is not known when the header is written,
// so small writes are buffered until either the threshold is crossed or the
// handler finishes.
type gzipResponseWriter struct {
	http.ResponseWriter

	status  int
	buf     []byte
	gz      *gzip.Writer
	started bool
	// passthrough is set when the handler encodes its own body, so the
	// wrapper stops touching it.
	passthrough bool
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	// Held back until the first write, when the size is known: the encoding
	// header cannot be set after the status is sent.
	w.status = status
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) {
	if w.gz != nil {
		return w.gz.Write(p)
	}
	if w.passthrough || w.Header().Get("Content-Encoding") != "" {
		// The handler encodes its own body, as the metrics handler does.
		// Compressing again would nest two gzip streams under one header.
		w.passthrough = true
		w.writeStatus()
		if len(w.buf) > 0 {
			if _, err := w.ResponseWriter.Write(w.buf); err != nil {
				return 0, err
			}
			w.buf = nil
		}
		return w.ResponseWriter.Write(p)
	}

	w.buf = append(w.buf, p...)
	if len(w.buf) < minCompressSize {
		return len(p), nil
	}

	w.startGzip()
	buffered := w.buf
	w.buf = nil
	if _, err := w.gz.Write(buffered); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush lets a handler push bytes to the client, compressing what is buffered
// so a streaming response is not held back waiting for the size threshold.
func (w *gzipResponseWriter) Flush() {
	if w.passthrough {
		if f, ok := w.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	if w.gz == nil && len(w.buf) > 0 {
		w.startGzip()
		buffered := w.buf
		w.buf = nil
		_, _ = w.gz.Write(buffered)
	}
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gzipResponseWriter) startGzip() {
	h := w.Header()
	h.Set("Content-Encoding", "gzip")
	// No handler here sets a length, but one that did would be describing the
	// uncompressed body, which would truncate the response.
	h.Del("Content-Length")
	w.writeStatus()

	gz, _ := gzipWriters.Get().(*gzip.Writer)
	gz.Reset(w.ResponseWriter)
	w.gz = gz
}

func (w *gzipResponseWriter) writeStatus() {
	if w.started {
		return
	}
	w.started = true
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(w.status)
}

// Close flushes whatever the handler produced. A response that never reached
// the threshold is sent uncompressed.
func (w *gzipResponseWriter) Close() {
	if w.gz != nil {
		_ = w.gz.Close()
		gzipWriters.Put(w.gz)
		w.gz = nil
		return
	}
	if w.passthrough {
		w.writeStatus()
		return
	}

	// Only an encoding this wrapper set is removed; a handler's own is left
	// alone by the passthrough above.
	w.Header().Del("Content-Encoding")
	w.writeStatus()
	if len(w.buf) > 0 {
		_, _ = w.ResponseWriter.Write(w.buf)
		w.buf = nil
	}
}
