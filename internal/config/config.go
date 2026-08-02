// Package config loads and validates the vmLookingGlass agent list.
package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultInterval = 60 * time.Second
	// DefaultTimeout covers fetching and decoding a large agent response.
	// A busy agent returns tens of megabytes, and decoding that costs seconds
	// of CPU on top of transfer, so the timeout is well above the fetch alone.
	DefaultTimeout = 120 * time.Second
	DefaultPort    = 8429
	DefaultListen  = ":8880"
)

// Config is the parsed contents of the config file.
type Config struct {
	// Listen is the HTTP address for the UI and API.
	Listen string `yaml:"listen"`
	// Interval is the default scrape-target poll interval for agents that do not override it.
	Interval time.Duration `yaml:"interval"`
	// Timeout is the default per-agent HTTP timeout for agents that do not override it.
	Timeout time.Duration `yaml:"timeout"`
	// MaxConcurrency caps how many agents are polled at once. Zero means unlimited.
	MaxConcurrency int     `yaml:"max_concurrency"`
	Agents         []Agent `yaml:"agents"`
}

// Agent is a single vmagent to poll.
type Agent struct {
	// Name identifies the agent in the UI and API. Defaults to the URL host.
	Name string `yaml:"name"`
	// URL is the agent base URL. A bare host or host:port is accepted and
	// normalized to http://host:port.
	URL string `yaml:"url"`
	// Labels are attached to every target reported by this agent, for grouping
	// agents by region or environment.
	Labels map[string]string `yaml:"labels"`

	// Interval overrides Config.Interval when non-zero.
	Interval time.Duration `yaml:"interval"`
	// Timeout overrides Config.Timeout when non-zero.
	Timeout time.Duration `yaml:"timeout"`
}

// Load reads and validates the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse validates and applies defaults to raw YAML config bytes.
func Parse(data []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	// Reject unknown keys so a typo'd field is an error rather than a silent default.
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.applyDefaults(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() error {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxConcurrency < 0 {
		return fmt.Errorf("max_concurrency must not be negative")
	}
	if len(c.Agents) == 0 {
		return fmt.Errorf("no agents configured")
	}

	seenName := make(map[string]int, len(c.Agents))
	seenURL := make(map[string]int, len(c.Agents))
	for i := range c.Agents {
		a := &c.Agents[i]
		if err := a.normalize(); err != nil {
			return fmt.Errorf("agent %d: %w", i, err)
		}
		if a.Interval <= 0 {
			a.Interval = c.Interval
		}
		if a.Timeout <= 0 {
			a.Timeout = c.Timeout
		}
		// A duplicate name would silently merge two agents' targets in the UI.
		if j, dup := seenName[a.Name]; dup {
			return fmt.Errorf("agent %d: duplicate name %q (also agent %d)", i, a.Name, j)
		}
		if j, dup := seenURL[a.URL]; dup {
			return fmt.Errorf("agent %d: duplicate url %q (also agent %d)", i, a.URL, j)
		}
		seenName[a.Name] = i
		seenURL[a.URL] = i
	}
	return nil
}

// Equal reports whether two agent configs are identical. Agent contains a map,
// so it cannot be compared with ==.
func (a Agent) Equal(b Agent) bool {
	if a.Name != b.Name || a.URL != b.URL ||
		a.Interval != b.Interval || a.Timeout != b.Timeout ||
		len(a.Labels) != len(b.Labels) {
		return false
	}
	for k, v := range a.Labels {
		if w, ok := b.Labels[k]; !ok || v != w {
			return false
		}
	}
	return true
}

// normalize expands shorthand URL forms and derives a default name.
func (a *Agent) normalize() error {
	if a.URL == "" {
		return fmt.Errorf("url is required")
	}
	raw := a.URL
	// Accept "host" and "host:port" shorthand alongside a full URL.
	if !hasScheme(raw) {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", a.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid url %q: unsupported scheme %q", a.URL, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid url %q: missing host", a.URL)
	}
	if u.Port() == "" {
		u.Host = fmt.Sprintf("%s:%d", u.Host, DefaultPort)
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	a.URL = u.String()

	if a.Name == "" {
		a.Name = u.Hostname()
	}
	return nil
}

func hasScheme(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ':':
			// A scheme separator is "://"; a bare "host:port" colon is not one.
			return i+2 < len(s) && s[i+1] == '/' && s[i+2] == '/'
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '+', c == '-', c == '.':
		default:
			return false
		}
	}
	return false
}
