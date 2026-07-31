package poll

import (
	"strings"
	"testing"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/model"
)

const oneActive = `{
  "status": "success",
  "data": {
    "activeTargets": [
      {
        "discoveredLabels": {"__address__": "10.0.0.1:9100", "job": "pool-a"},
        "labels": {"instance": "host1:9100", "job": "node"},
        "scrapePool": "pool-a",
        "scrapeUrl": "http://10.0.0.1:9100/metrics",
        "lastError": "",
        "lastScrape": "2026-08-02T05:35:48.825-04:00",
        "lastScrapeDuration": 0.393,
        "lastSamplesScraped": 11918,
        "health": "up"
      }
    ],
    "droppedTargets": [
      {"discoveredLabels": {"__address__": "10.0.0.1:9119", "job": "pool-b"}}
    ]
  }
}`

func TestDecodeActiveTarget(t *testing.T) {
	in := model.NewInterner()
	got, err := decodeTargets(strings.NewReader(oneActive), "agent01", in)
	if err != nil {
		t.Fatalf("decodeTargets: %v", err)
	}
	if len(got.Active) != 1 {
		t.Fatalf("active = %d, want 1", len(got.Active))
	}

	tgt := got.Active[0]
	if tgt.Agent != "agent01" {
		t.Errorf("Agent = %q, want agent01", tgt.Agent)
	}
	if tgt.ScrapePool != "pool-a" {
		t.Errorf("ScrapePool = %q, want pool-a", tgt.ScrapePool)
	}
	if tgt.ScrapeURL != "http://10.0.0.1:9100/metrics" {
		t.Errorf("ScrapeURL = %q", tgt.ScrapeURL)
	}
	if !tgt.IsUp() {
		t.Errorf("Health = %q, want up", tgt.Health)
	}
	if tgt.LastSamplesScraped != 11918 {
		t.Errorf("LastSamplesScraped = %d, want 11918", tgt.LastSamplesScraped)
	}
	if tgt.LastScrapeDuration != 0.393 {
		t.Errorf("LastScrapeDuration = %v, want 0.393", tgt.LastScrapeDuration)
	}
	if tgt.Labels.Get("instance") != "host1:9100" {
		t.Errorf("instance = %q", tgt.Labels.Get("instance"))
	}
	if tgt.DiscoveredLabels.Get("__address__") != "10.0.0.1:9100" {
		t.Errorf("__address__ = %q", tgt.DiscoveredLabels.Get("__address__"))
	}
	if tgt.Dropped {
		t.Error("active target marked dropped")
	}
	if tgt.LastScrape.IsZero() {
		t.Error("LastScrape not parsed")
	}
}

func TestDecodeDroppedTarget(t *testing.T) {
	in := model.NewInterner()
	got, err := decodeTargets(strings.NewReader(oneActive), "agent01", in)
	if err != nil {
		t.Fatalf("decodeTargets: %v", err)
	}
	if len(got.Dropped) != 1 {
		t.Fatalf("dropped = %d, want 1", len(got.Dropped))
	}

	tgt := got.Dropped[0]
	if !tgt.Dropped {
		t.Error("dropped target not marked dropped")
	}
	// vmagent omits scrapePool on dropped targets; it is recoverable from the
	// job key of the pre-relabel labels.
	if tgt.ScrapePool != "pool-b" {
		t.Errorf("ScrapePool = %q, want pool-b recovered from job label", tgt.ScrapePool)
	}
}

// Labels must go through the interner rather than being stored per target.
func TestDecodeInternsLabels(t *testing.T) {
	in := model.NewInterner()
	if _, err := decodeTargets(strings.NewReader(oneActive), "a", in); err != nil {
		t.Fatal(err)
	}
	before := in.Len()
	if before == 0 {
		t.Fatal("decode did not intern any labels")
	}
	// Decoding an identical payload must not grow the vocabulary.
	if _, err := decodeTargets(strings.NewReader(oneActive), "b", in); err != nil {
		t.Fatal(err)
	}
	if after := in.Len(); after != before {
		t.Errorf("interner grew from %d to %d on identical payload", before, after)
	}
}

func TestDecodeEmptyLists(t *testing.T) {
	in := model.NewInterner()
	got, err := decodeTargets(strings.NewReader(
		`{"status":"success","data":{"activeTargets":[],"droppedTargets":[]}}`), "a", in)
	if err != nil {
		t.Fatalf("decodeTargets: %v", err)
	}
	if len(got.Active) != 0 || len(got.Dropped) != 0 {
		t.Errorf("got %d active, %d dropped, want none", len(got.Active), len(got.Dropped))
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name, body string
	}{
		{"malformed json", `{"status":"success","data":{`},
		{"truncated mid-array", `{"data":{"activeTargets":[{"scrapePool":"a"}`},
		{"not an object", `[]`},
		{"empty body", ``},
		{"non-string object key", `{5:"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := model.NewInterner()
			if _, err := decodeTargets(strings.NewReader(tt.body), "a", in); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

// An agent reporting an error must surface it, even when the response also
// carries a well-formed but empty data object.
func TestDecodeErrorStatus(t *testing.T) {
	in := model.NewInterner()
	_, err := decodeTargets(strings.NewReader(
		`{"status":"error","errorType":"internal","error":"boom",
		  "data":{"activeTargets":[],"droppedTargets":[]}}`), "a", in)
	if err == nil {
		t.Fatal("expected error for status=error, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q does not carry the agent's message", err)
	}
}

// An unparseable timestamp must not discard the whole target.
func TestDecodeBadTimestampKeepsTarget(t *testing.T) {
	in := model.NewInterner()
	got, err := decodeTargets(strings.NewReader(`{"status":"success","data":{
      "activeTargets":[{"scrapePool":"p","scrapeUrl":"http://h/m","health":"up",
        "lastScrape":"not-a-timestamp","labels":{"instance":"h"}}],
      "droppedTargets":[]}}`), "a", in)
	if err != nil {
		t.Fatalf("decodeTargets: %v", err)
	}
	if len(got.Active) != 1 {
		t.Fatalf("active = %d, want 1", len(got.Active))
	}
	if !got.Active[0].LastScrape.IsZero() {
		t.Error("unparseable timestamp should leave LastScrape zero")
	}
}

// Timestamps arrive with an offset; the parsed instant must be correct.
func TestDecodeTimestampOffset(t *testing.T) {
	in := model.NewInterner()
	got, err := decodeTargets(strings.NewReader(`{"status":"success","data":{
      "activeTargets":[{"scrapePool":"p","scrapeUrl":"http://h/m","health":"up",
        "lastScrape":"2026-08-02T05:35:48.825-04:00","labels":{}}],
      "droppedTargets":[]}}`), "a", in)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 2, 9, 35, 48, 825000000, time.UTC)
	if !got.Active[0].LastScrape.UTC().Equal(want) {
		t.Errorf("LastScrape = %v, want %v", got.Active[0].LastScrape.UTC(), want)
	}
}
