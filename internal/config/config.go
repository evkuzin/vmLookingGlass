// Package config loads and validates the vmLookingGlass agent list.
package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"regexp"
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

	DefaultNetboxURL      = "https://netbox.hudson-trading.com/api"
	DefaultNetboxInterval = 30 * time.Minute
	DefaultNetboxTimeout  = 60 * time.Second
)

// Config is the parsed contents of the config file.
type Config struct {
	// Listen is the HTTP address for the UI and API.
	Listen string `yaml:"listen"`
	// Interval is the default scrape-target poll interval for agents that do not override it.
	Interval time.Duration `yaml:"interval"`
	// Timeout is the default per-agent HTTP timeout for agents that do not override it.
	Timeout time.Duration `yaml:"timeout"`
	// MaxConcurrency caps how many agents are polled at once. Zero means the
	// Go runtime's CPU budget (GOMAXPROCS), with a floor of two.
	MaxConcurrency int `yaml:"max_concurrency"`
	// Netbox, when set, discovers agents from the NetBox inventory in addition
	// to the static list.
	Netbox *Netbox `yaml:"netbox"`
	Agents []Agent `yaml:"agents"`
}

// Netbox configures agent discovery from the NetBox inventory.
type Netbox struct {
	// URL is the NetBox API root.
	URL string `yaml:"url"`
	// Tags are queried separately and their devices unioned.
	Tags []string `yaml:"tags"`
	// Exclude drops a device whose name matches this RE2 pattern.
	Exclude string `yaml:"exclude"`
	// Interval is the delay between refreshes.
	Interval time.Duration `yaml:"interval"`
	// Timeout bounds one whole refresh.
	Timeout time.Duration `yaml:"timeout"`
	// Labels are attached to every discovered agent.
	Labels map[string]string `yaml:"labels"`
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
	if c.Netbox != nil {
		if err := c.Netbox.applyDefaults(); err != nil {
			return err
		}
	}
	if len(c.Agents) == 0 && c.Netbox == nil {
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

// applyDefaults fills in the discovery defaults and validates the block.
func (n *Netbox) applyDefaults() error {
	if len(n.Tags) == 0 {
		return fmt.Errorf("netbox: tags is required")
	}
	if n.URL == "" {
		n.URL = DefaultNetboxURL
	}
	if n.Interval < 0 {
		return fmt.Errorf("netbox: interval must be positive")
	}
	if n.Interval == 0 {
		n.Interval = DefaultNetboxInterval
	}
	if n.Timeout < 0 {
		return fmt.Errorf("netbox: timeout must be positive")
	}
	if n.Timeout == 0 {
		n.Timeout = DefaultNetboxTimeout
	}
	if n.Exclude != "" {
		if _, err := regexp.Compile(n.Exclude); err != nil {
			return fmt.Errorf("netbox: exclude: %w", err)
		}
	}
	return nil
}

// MergeDiscovered returns a copy of file with the discovered names appended as
// agents after the static ones. A name whose derived name or URL is already
// taken by a static agent is skipped, so the static list wins. The input is
// shared by every watcher subscriber and is not modified.
func MergeDiscovered(file *Config, names []string) *Config {
	c := *file
	c.Agents = make([]Agent, len(file.Agents), len(file.Agents)+len(names))
	copy(c.Agents, file.Agents)
	if c.Netbox == nil {
		return &c
	}

	seenName := make(map[string]struct{}, len(c.Agents)+len(names))
	seenURL := make(map[string]struct{}, len(c.Agents)+len(names))
	for _, a := range c.Agents {
		seenName[a.Name] = struct{}{}
		seenURL[a.URL] = struct{}{}
	}

	for _, name := range names {
		a := Agent{URL: name, Labels: c.Netbox.Labels}
		if err := a.normalize(); err != nil {
			continue
		}
		if _, dup := seenName[a.Name]; dup {
			continue
		}
		if _, dup := seenURL[a.URL]; dup {
			continue
		}
		a.Interval = c.Interval
		a.Timeout = c.Timeout
		seenName[a.Name] = struct{}{}
		seenURL[a.URL] = struct{}{}
		c.Agents = append(c.Agents, a)
	}
	return &c
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

// Equal reports whether two NetBox blocks are identical. Netbox holds a slice
// and a map, so it cannot be compared with ==; a missing block differs from
// any block.
func (n *Netbox) Equal(o *Netbox) bool {
	if n == nil || o == nil {
		return n == o
	}
	if n.URL != o.URL || n.Exclude != o.Exclude ||
		n.Interval != o.Interval || n.Timeout != o.Timeout ||
		len(n.Tags) != len(o.Tags) || len(n.Labels) != len(o.Labels) {
		return false
	}
	for i, tag := range n.Tags {
		if o.Tags[i] != tag {
			return false
		}
	}
	for k, v := range n.Labels {
		if w, ok := o.Labels[k]; !ok || v != w {
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
	// vmagent's port is only assumed for plain HTTP. An https endpoint is
	// behind a load balancer or ingress rather than on vmagent's own
	// listener, so it keeps the scheme's default port. Either can be
	// overridden by naming the port in the URL.
	if u.Scheme == "http" && u.Port() == "" {
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
