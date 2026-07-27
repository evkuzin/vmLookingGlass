package config

import (
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	c, err := Parse([]byte(`
agents:
  - url: vmagent01
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Listen != DefaultListen {
		t.Errorf("Listen = %q, want %q", c.Listen, DefaultListen)
	}
	if c.Interval != DefaultInterval {
		t.Errorf("Interval = %v, want %v", c.Interval, DefaultInterval)
	}
	a := c.Agents[0]
	if want := "http://vmagent01:8429"; a.URL != want {
		t.Errorf("URL = %q, want %q", a.URL, want)
	}
	if a.Name != "vmagent01" {
		t.Errorf("Name = %q, want derived from host", a.Name)
	}
	if a.Interval != DefaultInterval || a.Timeout != DefaultTimeout {
		t.Errorf("agent did not inherit global interval/timeout: %v/%v", a.Interval, a.Timeout)
	}
}

func TestParseAgentOverrides(t *testing.T) {
	c, err := Parse([]byte(`
interval: 30s
timeout: 10s
agents:
  - name: near
    url: http://a:8429
  - name: far
    url: http://b:8429
    interval: 5m
    timeout: 90s
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := c.Agents[0].Interval; got != 30*time.Second {
		t.Errorf("near interval = %v, want inherited 30s", got)
	}
	if got := c.Agents[1].Interval; got != 5*time.Minute {
		t.Errorf("far interval = %v, want override 5m", got)
	}
	if got := c.Agents[1].Timeout; got != 90*time.Second {
		t.Errorf("far timeout = %v, want override 90s", got)
	}
}

func TestNormaliseURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"host", "http://host:8429"},
		{"host:9090", "http://host:9090"},
		{"http://host", "http://host:8429"},
		{"https://host:443", "https://host:443"},
		{"http://host:8429/prometheus", "http://host:8429"},
		{"http://host:8429?x=1", "http://host:8429"},
	}
	for _, tt := range tests {
		a := Agent{URL: tt.in}
		if err := a.normalise(); err != nil {
			t.Errorf("normalise(%q): %v", tt.in, err)
			continue
		}
		if a.URL != tt.want {
			t.Errorf("normalise(%q) = %q, want %q", tt.in, a.URL, tt.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"no agents", "interval: 30s\n"},
		{"empty agent list", "agents: []\n"},
		{"missing url", "agents:\n  - name: a\n"},
		{"bad scheme", "agents:\n  - url: ftp://host\n"},
		{"unknown field", "agents:\n  - url: host\n    frequency: 5s\n"},
		{"duplicate name", "agents:\n  - {name: a, url: h1}\n  - {name: a, url: h2}\n"},
		{"duplicate url", "agents:\n  - {name: a, url: h1}\n  - {name: b, url: h1}\n"},
		{"negative concurrency", "max_concurrency: -1\nagents:\n  - url: h\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.yaml)); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

// Two agents on different ports of one host are distinct and must be allowed.
func TestSameHostDifferentPorts(t *testing.T) {
	if _, err := Parse([]byte(`
agents:
  - url: host:8429
  - url: host:8430
`)); err == nil {
		t.Fatal("expected duplicate-name error: both agents derive the name from the same host")
	}

	c, err := Parse([]byte(`
agents:
  - {name: a, url: host:8429}
  - {name: b, url: host:8430}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.Agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(c.Agents))
	}
}
