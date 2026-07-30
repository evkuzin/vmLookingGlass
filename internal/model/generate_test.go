package model

import (
	"fmt"
	"math/rand/v2"
)

// Synthetic scrape-target payloads for tests and benchmarks.
//
// These imitate the shape of a real vmagent response rather than reproducing
// one: a large number of targets drawn from a small vocabulary of service
// discovery labels, most of which repeat on every target. That repetition is
// the property the interner exploits, so it is what the generator reproduces.

// genOptions controls the size and shape of a generated payload.
type genOptions struct {
	Active  int
	Dropped int
	// Hosts is the number of distinct hosts targets are spread across.
	Hosts int
	// Pools is the number of distinct scrape pools.
	Pools int
	// MetaLabels is how many discovery metadata labels each target carries.
	MetaLabels int
}

func defaultGenOptions() genOptions {
	return genOptions{Active: 2000, Dropped: 10000, Hosts: 400, Pools: 40, MetaLabels: 29}
}

// genTarget is one generated target, mirroring the fields the poller reads.
type genTarget struct {
	DiscoveredLabels map[string]string
	Labels           map[string]string
	ScrapePool       string
	ScrapeURL        string
	Health           string
}

// generateTargets builds a deterministic synthetic payload. The same seed
// always yields the same targets, so benchmarks stay comparable.
func generateTargets(seed uint64, o genOptions) []genTarget {
	r := rand.New(rand.NewPCG(seed, 0x9e3779b9))

	// Values drawn from small pools, as they are in a real fleet.
	envs := []string{"production", "staging", "dev"}
	regions := []string{"us-east", "us-west", "eu-central"}
	distros := []string{"bullseye", "bookworm", "trixie"}
	services := []string{"node_exporter", "redis_exporter", "blackbox", "windows_exporter", "vm_trade"}

	out := make([]genTarget, 0, o.Active+o.Dropped)
	for range o.Active + o.Dropped {
		// Address derives from the host index rather than being drawn at
		// random, so a host keeps one IP across every pool that scrapes it.
		hostIdx := r.IntN(o.Hosts)
		host := fmt.Sprintf("host%04d", hostIdx)
		pool := fmt.Sprintf("pool-%02d", r.IntN(o.Pools))
		// Port follows the service, as an exporter listens on a fixed port.
		svcIdx := r.IntN(len(services))
		svc := services[svcIdx]
		port := 9100 + svcIdx
		addr := fmt.Sprintf("10.0.%d.%d:%d", hostIdx/255, hostIdx%255, port)

		ip := fmt.Sprintf("10.0.%d.%d", hostIdx/255, hostIdx%255)
		disc := map[string]string{
			"__address__":         addr,
			"__metrics_path__":    "/metrics",
			"__scheme__":          "http",
			"__scrape_interval__": "1m0s",
			"__scrape_timeout__":  "1m0s",
			"job":                 pool,
			// Discovery reports a host's address under several keys, all
			// carrying the same value.
			"__meta_consul_address":            ip,
			"__meta_consul_tagged_address_lan": ip,
			"__meta_consul_tagged_address_wan": ip,
			"__meta_consul_metadata_nodename":  host,
			"__meta_consul_service":            svc,
			"__meta_consul_node":               host,
			"__meta_consul_dc":                 "primary",
			"__meta_consul_health":             "passing",
			"__meta_consul_metadata_cfenv":     envs[r.IntN(len(envs))],
			"__meta_consul_metadata_region":    regions[r.IntN(len(regions))],
			"__meta_consul_metadata_distro":    distros[r.IntN(len(distros))],
		}
		// Pad to the requested width with low-cardinality filler, matching the
		// tail of near-constant metadata labels a real payload carries.
		for n := len(disc); n < o.MetaLabels; n++ {
			disc[fmt.Sprintf("__meta_filler_%02d", n)] = fmt.Sprintf("v%d", r.IntN(4))
		}

		health := HealthUp
		if r.IntN(25) == 0 {
			health = HealthDown
		}
		out = append(out, genTarget{
			DiscoveredLabels: disc,
			Labels: map[string]string{
				"instance": fmt.Sprintf("%s:%d", host, port),
				"job":      svc,
				"env":      disc["__meta_consul_metadata_cfenv"],
			},
			ScrapePool: pool,
			ScrapeURL:  "http://" + addr + "/metrics",
			Health:     health,
		})
	}
	return out
}
