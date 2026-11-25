package config

import (
	"reflect"
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

func TestParseNetboxDefaults(t *testing.T) {
	c, err := Parse([]byte(`
netbox:
  tags: [a, b]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	n := c.Netbox
	if n.URL != DefaultNetboxURL {
		t.Errorf("URL = %q, want %q", n.URL, DefaultNetboxURL)
	}
	if n.Interval != DefaultNetboxInterval {
		t.Errorf("Interval = %v, want %v", n.Interval, DefaultNetboxInterval)
	}
	if n.Timeout != DefaultNetboxTimeout {
		t.Errorf("Timeout = %v, want %v", n.Timeout, DefaultNetboxTimeout)
	}
	if len(n.Tags) != 2 {
		t.Errorf("Tags = %v, want two", n.Tags)
	}
	if len(c.Agents) != 0 {
		t.Errorf("got %d static agents, want none to be required", len(c.Agents))
	}
}

func TestNetboxEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b *Netbox
		want bool
	}{
		{"both nil", nil, nil, true},
		{"one nil", nil, &Netbox{Tags: []string{"a"}}, false},
		{"nil versus empty labels", &Netbox{Tags: []string{"a"}}, &Netbox{Tags: []string{"a"}, Labels: map[string]string{}}, true},
		{"reordered tags", &Netbox{Tags: []string{"a", "b"}}, &Netbox{Tags: []string{"b", "a"}}, false},
		{"differing label value", &Netbox{Tags: []string{"a"}, Labels: map[string]string{"k": "v"}}, &Netbox{Tags: []string{"a"}, Labels: map[string]string{"k": "w"}}, false},
		{"identical", &Netbox{URL: "http://nb", Tags: []string{"a"}, Labels: map[string]string{"k": "v"}}, &Netbox{URL: "http://nb", Tags: []string{"a"}, Labels: map[string]string{"k": "v"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Equal(tt.b); got != tt.want {
				t.Errorf("Equal = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeDiscovered(t *testing.T) {
	file, err := Parse([]byte(`
interval: 30s
timeout: 5s
netbox:
  tags: [a]
  labels:
    source: netbox
agents:
  - name: static1
    url: static1
  - name: other
    url: host
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	before := append([]Agent(nil), file.Agents...)

	merged := MergeDiscovered(file, []string{"new1", "static1", "host"})

	if len(merged.Agents) != 3 {
		t.Fatalf("got %d agents, want 2 static + 1 discovered: %v", len(merged.Agents), merged.Agents)
	}
	if merged.Agents[0].Name != "static1" || merged.Agents[1].Name != "other" {
		t.Errorf("static agents not first: %v", merged.Agents)
	}
	d := merged.Agents[2]
	if d.Name != "new1" || d.URL != "http://new1:8429" {
		t.Errorf("discovered agent = %+v, want new1 on the default port", d)
	}
	if d.Interval != 30*time.Second || d.Timeout != 5*time.Second {
		t.Errorf("discovered interval/timeout = %v/%v, want the globals", d.Interval, d.Timeout)
	}
	if d.Labels["source"] != "netbox" {
		t.Errorf("discovered labels = %v, want the netbox labels", d.Labels)
	}

	if !reflect.DeepEqual(file.Agents, before) {
		t.Errorf("MergeDiscovered modified its input: %v", file.Agents)
	}
}

func TestMergeDiscoveredWithoutNetbox(t *testing.T) {
	file, err := Parse([]byte("agents:\n  - url: h\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	merged := MergeDiscovered(file, []string{"d1"})
	if len(merged.Agents) != 1 || merged.Agents[0].Name != "h" {
		t.Errorf("agents = %v, want only the static one", merged.Agents)
	}
}

func TestNormalizeURL(t *testing.T) {
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
		if err := a.normalize(); err != nil {
			t.Errorf("normalize(%q): %v", tt.in, err)
			continue
		}
		if a.URL != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.in, a.URL, tt.want)
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
		{"empty netbox", "netbox: {}\n"},
		{"netbox without tags", "netbox:\n  url: http://nb/api\n"},
		{"netbox bad exclude", "netbox:\n  tags: [a]\n  exclude: '('\n"},
		{"netbox negative interval", "netbox:\n  tags: [a]\n  interval: -1s\n"},
		{"netbox negative timeout", "netbox:\n  tags: [a]\n  timeout: -1s\n"},
		{"netbox unknown field", "netbox:\n  tags: [a]\n  tag: b\n"},
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

// The default port belongs to vmagent's own listener. A URL that named a
// scheme but no port was getting it too, so an https endpoint behind a load
// balancer was addressed on the wrong port.
func TestSchemeDefaultPort(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://prom.example", "https://prom.example"},
		{"http://prom.example", "http://prom.example:8429"},
		{"https://prom.example:9090", "https://prom.example:9090"},
		{"https://prom.example:443", "https://prom.example:443"},
		// Shorthand still means a vmagent, which listens on its own port.
		{"host", "http://host:8429"},
		{"host:9090", "http://host:9090"},
	}
	for _, tt := range tests {
		a := Agent{URL: tt.in}
		if err := a.normalize(); err != nil {
			t.Errorf("normalize(%q): %v", tt.in, err)
			continue
		}
		if a.URL != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.in, a.URL, tt.want)
		}
	}
}
