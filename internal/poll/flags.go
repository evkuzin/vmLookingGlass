package poll

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/evkuzin/vmLookingGlass/internal/config"
)

// DefaultMaxDroppedTargets is vmagent's default -promscrape.maxDroppedTargets.
const DefaultMaxDroppedTargets = 10000

const (
	flagsPath           = "/flags"
	maxDroppedFlagName  = "promscrape.maxDroppedTargets"
	maxFlagsResponseLen = 1 << 20
)

// fetchMaxDroppedTargets reads the agent's dropped-target cap from /flags.
//
// The cap is needed to tell a truncated dropped list from a complete one: the
// targets API reports neither the cap nor the true total, so the only signal
// is the returned count reaching the configured maximum.
//
// Any failure yields the default. The endpoint can be missing, behind an auth
// key, or the agent simply unreachable, and none of that should fail a poll.
func (p *Poller) fetchMaxDroppedTargets(ctx context.Context, a config.Agent) int {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL+flagsPath, http.NoBody)
	if err != nil {
		return DefaultMaxDroppedTargets
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return DefaultMaxDroppedTargets
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return DefaultMaxDroppedTargets
	}
	return parseMaxDroppedTargets(io.LimitReader(resp.Body, maxFlagsResponseLen))
}

// parseMaxDroppedTargets reads the cap from a /flags body.
//
// vmagent lists only flags that were explicitly set, one per line as
// -name="value", so the flag being absent means the agent runs the default.
func parseMaxDroppedTargets(r io.Reader) int {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		name, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok || strings.TrimPrefix(name, "-") != maxDroppedFlagName {
			continue
		}
		n, err := strconv.Atoi(strings.Trim(value, `"`))
		if err != nil || n <= 0 {
			return DefaultMaxDroppedTargets
		}
		return n
	}
	return DefaultMaxDroppedTargets
}
