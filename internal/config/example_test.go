package config

import (
	"path/filepath"
	"testing"
	"time"
)

// The shipped example is the first thing anyone copies, so it has to parse and
// to exercise every option. This fails if an option is renamed without the
// example being updated.
func TestExampleConfigIsValid(t *testing.T) {
	path := filepath.Join("..", "..", "misc", "config.example.yml")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("example config does not load: %v", err)
	}

	if len(c.Agents) < 2 {
		t.Fatalf("example has %d agents; it should show more than one form", len(c.Agents))
	}

	byName := make(map[string]Agent, len(c.Agents))
	for _, a := range c.Agents {
		byName[a.Name] = a
	}

	// Shorthand host, expanded to a full URL with the default port.
	if got := byName["vmagent01"].URL; got != "http://vmagent01:8429" {
		t.Errorf("shorthand url expanded to %q", got)
	}

	var sawOverride, sawLabels, sawInherit bool
	for _, a := range c.Agents {
		if a.Interval != c.Interval || a.Timeout != c.Timeout {
			sawOverride = true
		}
		if a.Interval == c.Interval && a.Timeout == c.Timeout {
			sawInherit = true
		}
		if len(a.Labels) > 0 {
			sawLabels = true
		}
	}
	if !sawOverride {
		t.Error("example does not demonstrate a per-agent interval or timeout override")
	}
	if !sawInherit {
		t.Error("example does not demonstrate an agent inheriting the defaults")
	}
	if !sawLabels {
		t.Error("example does not demonstrate agent labels")
	}
	if c.Interval <= 0 || c.Timeout <= 0 {
		t.Errorf("example has non-positive defaults: %v / %v", c.Interval, c.Timeout)
	}
	if c.Interval < time.Second {
		t.Errorf("example interval %v is unrealistically short", c.Interval)
	}
}
