# vmLookingGlass

Aggregates scrape target status from many vmagents into one searchable view.

Each vmagent exposes its own targets at `/api/v1/targets`, so answering "what
is scraping this host?" means asking every agent in turn. vmLookingGlass polls
them all on a schedule and serves the union, with the scraping agent attached
to every target.

## What it gives you

- One target list across the fleet, with the agent that scrapes each target.
- Targets scraped by more than one agent are merged into a single row and
  flagged, which is how accidental double-scraping becomes visible.
- Dropped targets alongside active ones, for debugging relabel rules.
- A search API and CLI using the filter syntax you already use.
- A Prometheus-compatible `/api/v1/targets`, so existing tooling can point at
  the aggregator instead of a single agent.

## Running

```sh
vmlookingglass -config /etc/vmLookingGlass/config.yml
```

See `misc/config.example.yml` for the configuration format and
`misc/vmlookingglass.service` for a systemd unit.

The config file is watched: agents that appear start being polled, agents that
disappear stop, and agents whose settings are unchanged keep going. A file that
fails to parse is logged and ignored, leaving the previous config in effect.
Generate it from whatever inventory owns the agent list, writing to a temporary
file and renaming over the target.

## Querying

```sh
vmlg health=down                             # every failing target, fleet-wide
vmlg labels.job=node agent=vmagent01     # filters are ANDed
vmlg -o csv -columns instance,agents,multi   # pick the columns you want
vmlg -template '{{.Instance}} {{.Agents}}'   # or format them yourself
vmlg -agents                                 # per-agent poll status
```

Filters match the syntax of the existing standalone finder:

| Filter | Meaning |
| --- | --- |
| `labels.<name>=<regex>` | label matches the whole pattern |
| `labels.<name>=~<regex>` | label contains a match |
| `labels.<name>!~<regex>` | label does not match, including when absent |
| `scrapePool=<regex>` | scrape pool matches the whole pattern |
| `agent=<regex>` | any agent scraping the target matches |
| `health=up\|down\|unknown` | exact health, not a pattern |
| `state=active\|dropped\|any` | target state |
| `lastScrapeDuration>0.5` | also `<`, `>=`, `<=`, `==`, `!=` |
| `lastSamplesScraped>1000` | as above, integer |

## API

| Endpoint | Purpose |
| --- | --- |
| `GET /api/v1/targets` | Prometheus-shaped union, plus `agent` and `agents` per target |
| `GET /api/v1/scrape_pools` | every pool name seen |
| `GET /api/v1/search` | filtered and paged targets |
| `GET /api/v1/agents` | per-agent poll status and staleness |
| `GET /-/healthy`, `/-/ready` | liveness and readiness |

`/api/v1/search` is paged and reports the full match count, so a broad query
returns quickly. `/api/v1/targets` is deliberately uncapped: a client written
against the Prometheus API cannot tell a truncated response from a complete
one, so narrow it with `state` and `scrapePool` instead.

## A note on dropped targets

vmagent caps its dropped-target list at `-promscrape.maxDroppedTargets`
(10000 by default) and evicts arbitrarily once full, so what you see is a
partial and shifting subset. The list is also cumulative rather than a current
snapshot: a target dropped once stays listed. Treat dropped targets as a
debugging aid, not an inventory.

## Building

```sh
make                    # binaries with the embedded UI, into bin/
make test
```

On a network that TLS-inspects outbound traffic, the interception cert is not
in the base images' trust store and the build cannot fetch dependencies. Point
`BUILD_CA` at a bundle that includes it:

```sh
cp .env.example .env    # then set BUILD_CA
```

Everywhere else no certificate is needed and `make` works as-is.

The build runs in a container: the UI needs a Node toolchain whose version
matters, and building it there keeps the result independent of whatever is
installed on the host. BuildKit writes the binaries straight to `bin/`, so no
image is left behind.

`make build-host` compiles with the local Go toolchain instead. That skips the
UI, so the daemon serves the API only and says so at startup; it is for quick
iteration on the daemon, not for producing something to ship.

For a runnable container image rather than binaries:

```sh
docker build --target runtime -t vmlookingglass .
```
