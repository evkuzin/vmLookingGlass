// Package poll fetches scrape target status from vmagents.
package poll

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/model"
)

// Result is one agent's decoded target lists.
type Result struct {
	Active  []model.Target
	Dropped []model.Target
}

// rawTarget mirrors one entry of activeTargets or droppedTargets.
//
// Dropped entries carry only discoveredLabels; the remaining fields stay at
// their zero values.
type rawTarget struct {
	DiscoveredLabels   map[string]string `json:"discoveredLabels"`
	Labels             map[string]string `json:"labels"`
	ScrapePool         string            `json:"scrapePool"`
	ScrapeURL          string            `json:"scrapeUrl"`
	LastError          string            `json:"lastError"`
	LastScrape         string            `json:"lastScrape"`
	LastScrapeDuration float64           `json:"lastScrapeDuration"`
	LastSamplesScraped int64             `json:"lastSamplesScraped"`
	Health             string            `json:"health"`
}

// decodeTargets reads an /api/v1/targets response and converts it to targets
// attributed to agent.
//
// The response is streamed token by token: a single agent's response runs to
// tens of megabytes, so target objects are converted and their transient maps
// released one at a time rather than held as one decoded tree.
func decodeTargets(r io.Reader, agent string, in *model.Interner) (*Result, error) {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return nil, fmt.Errorf("response root: %w", err)
	}

	res := &Result{}
	var status, errMsg string
	var sawData bool

	for dec.More() {
		key, err := readKey(dec)
		if err != nil {
			return nil, err
		}
		switch key {
		case "status":
			if err := dec.Decode(&status); err != nil {
				return nil, fmt.Errorf("status: %w", err)
			}
		case "error":
			if err := dec.Decode(&errMsg); err != nil {
				return nil, fmt.Errorf("error field: %w", err)
			}
		case "data":
			sawData = true
			if err := decodeData(dec, agent, in, res); err != nil {
				return nil, err
			}
		default:
			if err := skipValue(dec); err != nil {
				return nil, err
			}
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return nil, fmt.Errorf("response close: %w", err)
	}

	// An agent reporting an error returns no usable data; surfacing its message
	// beats reporting the agent as empty.
	if status != "" && status != "success" {
		if errMsg == "" {
			errMsg = status
		}
		return nil, fmt.Errorf("agent reported %s: %s", status, errMsg)
	}
	if !sawData {
		return nil, fmt.Errorf("response has no data object")
	}
	return res, nil
}

func decodeData(dec *json.Decoder, agent string, in *model.Interner, res *Result) error {
	if err := expectDelim(dec, '{'); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	for dec.More() {
		key, err := readKey(dec)
		if err != nil {
			return err
		}
		switch key {
		case "activeTargets":
			res.Active, err = decodeArray(dec, agent, in, false)
		case "droppedTargets":
			res.Dropped, err = decodeArray(dec, agent, in, true)
		default:
			err = skipValue(dec)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return expectDelim(dec, '}')
}

func decodeArray(dec *json.Decoder, agent string, in *model.Interner, dropped bool) ([]model.Target, error) {
	if err := expectDelim(dec, '['); err != nil {
		return nil, err
	}
	var out []model.Target
	for dec.More() {
		var raw rawTarget
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw.toTarget(agent, in, dropped))
	}
	if err := expectDelim(dec, ']'); err != nil {
		return nil, err
	}
	return out, nil
}

func (raw *rawTarget) toTarget(agent string, in *model.Interner, dropped bool) model.Target {
	t := model.Target{
		Agent:              agent,
		ScrapePool:         raw.ScrapePool,
		ScrapeURL:          raw.ScrapeURL,
		Labels:             in.Labels(raw.Labels),
		DiscoveredLabels:   in.Labels(raw.DiscoveredLabels),
		Health:             raw.Health,
		LastError:          raw.LastError,
		LastScrapeDuration: raw.LastScrapeDuration,
		LastSamplesScraped: raw.LastSamplesScraped,
		Dropped:            dropped,
	}
	if raw.LastScrape != "" {
		// A malformed timestamp leaves the zero value rather than dropping the
		// target, which still carries useful identity and label data.
		if ts, err := time.Parse(time.RFC3339Nano, raw.LastScrape); err == nil {
			t.LastScrape = ts
		}
	}
	if dropped {
		t.Health = model.HealthUnknown
		// Dropped entries omit scrapePool, but the pre-relabel job label holds
		// the same value.
		if t.ScrapePool == "" {
			t.ScrapePool = t.DiscoveredLabels.Get("job")
		}
	}
	return t
}

// readKey reads an object key, erroring if the next token is not a string.
func readKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	key, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("expected object key, got %v", tok)
	}
	return key, nil
}

func expectDelim(dec *json.Decoder, want rune) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	got, ok := tok.(json.Delim)
	if !ok || rune(got) != want {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

// skipValue discards the next value, descending through nested structures so
// unknown fields do not desynchronise the token stream.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	open, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	var closing rune
	switch open {
	case '{':
		closing = '}'
	case '[':
		closing = ']'
	default:
		return fmt.Errorf("unexpected delimiter %v", open)
	}
	for dec.More() {
		if err := skipValue(dec); err != nil {
			return err
		}
	}
	return expectDelim(dec, closing)
}
