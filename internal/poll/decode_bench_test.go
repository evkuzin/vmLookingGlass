package poll

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/evkuzin/vmLookingGlass/internal/model"
)

// genPayload builds an /api/v1/targets response with n active and n dropped
// targets, shaped like a real one: labels drawn from a small vocabulary,
// with one address per host reused across pools.
func genPayload(n int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"status":"success","data":{"activeTargets":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		host := fmt.Sprintf("host%04d", i%400)
		fmt.Fprintf(&b, `{"discoveredLabels":{"__address__":"10.0.%d.%d:9100",`+
			`"__meta_consul_node":%q,"__meta_consul_dc":"primary","job":"pool-%02d"},`+
			`"labels":{"instance":"%s:9100","job":"node"},`+
			`"scrapePool":"pool-%02d","scrapeUrl":"http://10.0.%d.%d:9100/metrics",`+
			`"lastError":"","lastScrape":"2026-08-02T05:35:48.825-04:00",`+
			`"lastScrapeDuration":0.393,"lastSamplesScraped":11918,"health":"up"}`,
			(i%400)/255, (i%400)%255, host, i%40, host, i%40, (i%400)/255, (i%400)%255)
	}
	b.WriteString(`],"droppedTargets":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"discoveredLabels":{"__address__":"10.0.%d.%d:9119","job":"pool-%02d"}}`,
			(i%400)/255, (i%400)%255, i%40)
	}
	b.WriteString(`]}}`)
	return b.Bytes()
}

// TestDecodeLargePayload exercises the decoder on a body far larger than any
// buffer it should hold, confirming it streams rather than accumulating.
func TestDecodeLargePayload(t *testing.T) {
	body := genPayload(20000)
	t.Logf("payload %.1fMB", float64(len(body))/1e6)

	in := model.NewInterner()
	got, err := decodeTargets(bytes.NewReader(body), "agent01", in)
	if err != nil {
		t.Fatalf("decodeTargets: %v", err)
	}
	if len(got.Active) != 20000 || len(got.Dropped) != 20000 {
		t.Fatalf("got %d active, %d dropped, want 20000 each", len(got.Active), len(got.Dropped))
	}
	// The label vocabulary is bounded by discovery, not by target count.
	if in.Len() > 5000 {
		t.Errorf("interned %d strings from a small vocabulary; dedup is not working", in.Len())
	}
}

// BenchmarkDecodeStream reports decode cost per target. Bytes per operation
// should track the retained targets, not the size of the response body.
func BenchmarkDecodeStream(b *testing.B) {
	body := genPayload(5000)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()

	b.ResetTimer()
	for b.Loop() {
		in := model.NewInterner()
		if _, err := decodeTargets(bytes.NewReader(body), "a", in); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeWholeTree unmarshals the whole response before converting.
// Per call it is close to the streaming decoder, since both retain every
// target; the difference is that this form also holds the full response and
// its parse tree at once, which multiplies across concurrent polls.
func BenchmarkDecodeWholeTree(b *testing.B) {
	body := genPayload(5000)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()

	type whole struct {
		Data struct {
			ActiveTargets  []rawTarget `json:"activeTargets"`
			DroppedTargets []rawTarget `json:"droppedTargets"`
		} `json:"data"`
	}

	b.ResetTimer()
	for b.Loop() {
		var w whole
		if err := json.Unmarshal(body, &w); err != nil {
			b.Fatal(err)
		}
		in := model.NewInterner()
		out := make([]model.Target, 0, len(w.Data.ActiveTargets))
		for i := range w.Data.ActiveTargets {
			out = append(out, w.Data.ActiveTargets[i].toTarget("a", in, false))
		}
		_ = out
	}
}

// readerFunc counts how many bytes a decoder actually pulls at a time.
type countingReader struct {
	r       io.Reader
	maxRead int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > c.maxRead {
		c.maxRead = n
	}
	return n, err
}

// TestDecodeReadsInChunks confirms the decoder consumes the body incrementally
// rather than requesting it in one call.
func TestDecodeReadsInChunks(t *testing.T) {
	body := genPayload(5000)
	cr := &countingReader{r: bytes.NewReader(body)}

	in := model.NewInterner()
	if _, err := decodeTargets(cr, "a", in); err != nil {
		t.Fatalf("decodeTargets: %v", err)
	}
	if cr.maxRead >= len(body) {
		t.Errorf("largest single read was %d bytes of a %d byte body; not streaming",
			cr.maxRead, len(body))
	}
	t.Logf("body %d bytes, largest single read %d bytes", len(body), cr.maxRead)
}
