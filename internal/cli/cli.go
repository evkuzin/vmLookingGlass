// Package cli implements the vmlg command, a client for the aggregated
// target API.
package cli

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"text/template"
	"time"
)

const defaultTimeout = 30 * time.Second

// Row is one target as the CLI formats it. Field names are the ones a
// -template refers to.
type Row struct {
	Instance   string
	Job        string
	ScrapePool string
	ScrapeURL  string
	Health     string
	Agents     string
	Agent      string
	Error      string
	Duration   float64
	Samples    int64
	Multi      bool
	Disputed   bool

	Labels           map[string]string
	DiscoveredLabels map[string]string
	// HealthByAgent is populated only when the agents disagree.
	HealthByAgent map[string]string
}

// column is a named accessor, so -columns and the default table share one
// definition of what each column contains.
type column struct {
	name  string
	value func(*Row) string
}

var columns = []column{
	{"instance", func(r *Row) string { return r.Instance }},
	{"job", func(r *Row) string { return r.Job }},
	{"pool", func(r *Row) string { return r.ScrapePool }},
	{"health", func(r *Row) string { return r.Health }},
	{"agents", func(r *Row) string { return r.Agents }},
	{"url", func(r *Row) string { return r.ScrapeURL }},
	{"error", func(r *Row) string { return r.Error }},
	{"duration", func(r *Row) string { return fmt.Sprintf("%.3f", r.Duration) }},
	{"samples", func(r *Row) string { return fmt.Sprintf("%d", r.Samples) }},
	{"multi", func(r *Row) string { return fmt.Sprintf("%t", r.Multi) }},
	{"disputed", func(r *Row) string { return fmt.Sprintf("%t", r.Disputed) }},
}

var defaultColumns = []string{"instance", "job", "pool", "health", "agents"}

func columnByName(name string) (column, bool) {
	for _, c := range columns {
		if c.name == name {
			return c, true
		}
	}
	return column{}, false
}

func columnNames() string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = c.name
	}
	return strings.Join(names, ", ")
}

// usage explains the parts of the filter syntax that are not guessable:
// filters combine, labels need a prefix, and values are bare regexes whose
// anchoring differs between = and =~.
const usage = `usage: vmlg [flags] [filter ...]

Several filters combine, and a target has to match all of them:

  vmlg labels.job=node health=down

By label. Every label needs the labels. prefix, including instance and job:

  labels.instance=host042:9100   the whole value
  labels.instance=~042           contains it anywhere
  labels.job!=redis              is anything else, or has no such label
  labels.job!~redis              does not contain it, or has no such label
  labels.env=prod|staging        either, since the value is a regex

By target:

  health=down                    up, down or unknown
  state=dropped                  active, dropped or any
  agent=vmagent01                any agent scraping the target
  scrapePool=consul.*            the whole name, so anchor with .*
  lastScrapeDuration>1           seconds; also < >= <= == !=
  lastSamplesScraped<100         sample count

Values are regular expressions and are not quoted. = has to match the whole
value, so prefer =~ for a substring rather than wrapping it in .* yourself.

Flags:
`

// filterList collects a repeatable -filter flag.
type filterList []string

func (f *filterList) String() string     { return strings.Join(*f, " ") }
func (f *filterList) Set(v string) error { *f = append(*f, v); return nil }

// Run executes the command with the given arguments, writing to out.
func Run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("vmlg", flag.ContinueOnError)
	fs.SetOutput(out)

	filters := filterList{}
	fs.Var(&filters, "filter", "filter expression; repeatable, also accepted positionally")

	addr := fs.String("addr", "http://localhost:8880", "vmlookingglass address")
	format := fs.String("o", "table", "output format: table, json or csv")
	cols := fs.String("columns", strings.Join(defaultColumns, ","),
		"comma-separated columns: "+columnNames())
	tmpl := fs.String("template", "", "Go template applied per target, overriding -o")
	noHeader := fs.Bool("no-header", false, "omit the header row")
	limit := fs.Int("limit", 0, "maximum targets to return, 0 for the server default")
	offset := fs.Int("offset", 0, "skip this many matches")
	agents := fs.Bool("agents", false, "list agents instead of targets")
	timeout := fs.Duration("timeout", defaultTimeout, "request timeout")

	fs.Usage = func() {
		fmt.Fprint(out, usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	base, err := normalizeAddr(*addr)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: *timeout}

	if *agents {
		return runAgents(client, base, out)
	}
	// Flag and positional filters combine, so a filter can go anywhere on
	// the command line.
	all := make([]string, 0, len(filters)+len(fs.Args()))
	all = append(all, filters...)
	all = append(all, fs.Args()...)
	return runSearch(client, base, all,
		*format, *cols, *tmpl, *noHeader, *limit, *offset, out)
}

func runSearch(client *http.Client, base string, filters []string,
	format, cols, tmpl string, noHeader bool, limit, offset int, out io.Writer) error {
	q := url.Values{}
	for _, f := range filters {
		q.Add("filter", f)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	if offset > 0 {
		q.Set("offset", fmt.Sprint(offset))
	}

	var resp struct {
		Targets []struct {
			Labels             map[string]string `json:"labels"`
			DiscoveredLabels   map[string]string `json:"discoveredLabels"`
			ScrapePool         string            `json:"scrapePool"`
			ScrapeURL          string            `json:"scrapeUrl"`
			Health             string            `json:"health"`
			LastError          string            `json:"lastError"`
			LastScrapeDuration float64           `json:"lastScrapeDuration"`
			LastSamplesScraped int64             `json:"lastSamplesScraped"`
			Agents             []string          `json:"agents"`
			MultiScraped       bool              `json:"multiScraped"`
			Disputed           bool              `json:"disputed"`
			HealthByAgent      map[string]string `json:"healthByAgent"`
		} `json:"targets"`
		Total     int  `json:"total"`
		Truncated bool `json:"truncated"`
	}
	raw, err := fetch(client, base+"/api/v1/search", q, &resp)
	if err != nil {
		return err
	}

	if format == "json" && tmpl == "" {
		_, err := out.Write(raw)
		return err
	}

	rows := make([]*Row, 0, len(resp.Targets))
	for _, t := range resp.Targets {
		agent := ""
		if len(t.Agents) > 0 {
			agent = t.Agents[0]
		}
		rows = append(rows, &Row{
			Instance:         instanceOf(t.Labels, t.DiscoveredLabels),
			Job:              firstOf(t.Labels["job"], t.DiscoveredLabels["job"]),
			ScrapePool:       t.ScrapePool,
			ScrapeURL:        t.ScrapeURL,
			Health:           t.Health,
			Agents:           strings.Join(t.Agents, ","),
			Agent:            agent,
			Error:            t.LastError,
			Duration:         t.LastScrapeDuration,
			Samples:          t.LastSamplesScraped,
			Multi:            t.MultiScraped,
			Disputed:         t.Disputed,
			HealthByAgent:    t.HealthByAgent,
			Labels:           t.Labels,
			DiscoveredLabels: t.DiscoveredLabels,
		})
	}

	if tmpl != "" {
		return renderTemplate(tmpl, rows, out)
	}

	selected, err := selectColumns(cols)
	if err != nil {
		return err
	}

	switch format {
	case "table":
		if len(rows) == 0 {
			fmt.Fprintln(out, "no targets matched")
			return nil
		}
		if err := renderTable(selected, rows, noHeader, out); err != nil {
			return err
		}
		fmt.Fprintf(out, "\n%d shown of %d matched", len(rows), resp.Total)
		if resp.Truncated {
			fmt.Fprint(out, " (truncated, raise -limit or narrow the filter)")
		}
		fmt.Fprintln(out)
		return nil
	case "csv":
		return renderCSV(selected, rows, noHeader, out)
	}
	return fmt.Errorf("unknown output format %q: use table, json or csv", format)
}

// instanceOf names a target for display. A dropped target was relabeled away
// before it got an instance label, so fall back to the address it was
// discovered at rather than showing a blank row.
func instanceOf(labels, discovered map[string]string) string {
	return firstOf(labels["instance"], discovered["__address__"])
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func selectColumns(spec string) ([]column, error) {
	var out []column
	for _, name := range strings.Split(spec, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		c, ok := columnByName(name)
		if !ok {
			return nil, fmt.Errorf("unknown column %q: available are %s", name, columnNames())
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no columns selected")
	}
	return out, nil
}

func renderTable(cols []column, rows []*Row, noHeader bool, out io.Writer) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if !noHeader {
		head := make([]string, len(cols))
		for i, c := range cols {
			head[i] = strings.ToUpper(c.name)
		}
		fmt.Fprintln(tw, strings.Join(head, "\t"))
	}
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = c.value(r)
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	return tw.Flush()
}

func renderCSV(cols []column, rows []*Row, noHeader bool, out io.Writer) error {
	w := csv.NewWriter(out)
	if !noHeader {
		head := make([]string, len(cols))
		for i, c := range cols {
			head[i] = c.name
		}
		if err := w.Write(head); err != nil {
			return err
		}
	}
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = c.value(r)
		}
		if err := w.Write(cells); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func renderTemplate(text string, rows []*Row, out io.Writer) error {
	// A struct field that does not exist fails at execution, so a typo in a
	// template is reported rather than printing blank output. A missing map
	// key is not an error: label sets legitimately differ between targets.
	t, err := template.New("row").Parse(text)
	if err != nil {
		return fmt.Errorf("invalid template: %w", err)
	}
	for _, r := range rows {
		if err := t.Execute(out, r); err != nil {
			return fmt.Errorf("template: %w", err)
		}
		fmt.Fprintln(out)
	}
	return nil
}

func runAgents(client *http.Client, base string, out io.Writer) error {
	var resp struct {
		Agents []struct {
			Name           string `json:"name"`
			URL            string `json:"url"`
			Error          string `json:"error"`
			DurationMS     int64  `json:"durationMs"`
			ActiveTargets  int    `json:"activeTargets"`
			DroppedTargets int    `json:"droppedTargets"`
			UpTargets      int    `json:"upTargets"`
			DownTargets    int    `json:"downTargets"`
			Healthy        bool   `json:"healthy"`
			Stale          bool   `json:"stale"`
		} `json:"agents"`
		ActiveCount  int `json:"activeCount"`
		DroppedCount int `json:"droppedCount"`
		UpCount      int `json:"upCount"`
		DownCount    int `json:"downCount"`
	}
	if _, err := fetch(client, base+"/api/v1/agents", nil, &resp); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tSTATE\tACTIVE\tUP\tDOWN\tDROPPED\tPOLL\tERROR")
	for _, a := range resp.Agents {
		state := "ok"
		switch {
		case !a.Healthy:
			state = "failing"
		case a.Stale:
			state = "stale"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%dms\t%s\n",
			a.Name, state, a.ActiveTargets, a.UpTargets, a.DownTargets,
			a.DroppedTargets, a.DurationMS, a.Error)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%d agents, %d active targets (%d up, %d down), %d dropped\n",
		len(resp.Agents), resp.ActiveCount, resp.UpCount, resp.DownCount, resp.DroppedCount)
	return nil
}

// fetch performs a GET and decodes the data object into v, returning the raw
// data JSON so callers can pass it through unchanged.
func fetch(client *http.Client, endpoint string, q url.Values, v any) ([]byte, error) {
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Status string          `json:"status"`
		Error  string          `json:"error"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("http %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if envelope.Status != "success" {
		if envelope.Error != "" {
			return nil, fmt.Errorf("%s", envelope.Error)
		}
		return nil, fmt.Errorf("http %s", resp.Status)
	}
	if err := json.Unmarshal(envelope.Data, v); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return envelope.Data, nil
}

// normalizeAddr accepts a bare host, host:port or a full URL.
func normalizeAddr(addr string) (string, error) {
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil {
		return "", fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid address %q: missing host", addr)
	}
	return strings.TrimSuffix(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}
