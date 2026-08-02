// Package model holds the aggregated target types and the label interner.
package model

import (
	"hash/maphash"
	"slices"
	"strings"
	"sync"
)

// internShards is the number of independent maps the interner is split
// across. Every agent interns the whole fleet vocabulary on every poll, so a
// single lock serializes the hot path; spreading keys over many locks keeps
// throughput rising with concurrency instead of collapsing.
const internShards = 64

type internShard struct {
	mu sync.RWMutex
	m  map[string]string
	// Pad to a cache line so adjacent shards' locks do not share one, which
	// would reintroduce contention through false sharing.
	_ [40]byte
}

// Interner deduplicates label strings across agents.
//
// Scrape target metadata repeats heavily: a fleet-wide poll contains hundreds
// of thousands of label pairs drawn from only a few thousand distinct strings,
// so canonicalizing them collapses label memory by two orders of magnitude.
//
// Entries are never evicted. The distinct-string set is bounded by the label
// vocabulary of the monitored fleet, not by target count, so it converges.
type Interner struct {
	shards [internShards]internShard
	seed   maphash.Seed
}

func NewInterner() *Interner {
	in := &Interner{seed: maphash.MakeSeed()}
	for i := range in.shards {
		in.shards[i].m = make(map[string]string)
	}
	return in
}

func (in *Interner) shard(s string) *internShard {
	return &in.shards[maphash.String(in.seed, s)%internShards]
}

// String returns the canonical instance of s, storing it if not yet seen.
func (in *Interner) String(s string) string {
	if s == "" {
		return ""
	}
	sh := in.shard(s)

	sh.mu.RLock()
	got, ok := sh.m[s]
	sh.mu.RUnlock()
	if ok {
		return got
	}

	sh.mu.Lock()
	defer sh.mu.Unlock()
	// Another goroutine may have stored it between the two locks.
	if got, ok := sh.m[s]; ok {
		return got
	}
	sh.m[s] = s
	return s
}

// Len returns the number of distinct interned strings.
func (in *Interner) Len() int {
	n := 0
	for i := range in.shards {
		sh := &in.shards[i]
		sh.mu.RLock()
		n += len(sh.m)
		sh.mu.RUnlock()
	}
	return n
}

// Labels interns every name and value in m and returns them as a Labels set.
func (in *Interner) Labels(m map[string]string) Labels {
	if len(m) == 0 {
		return nil
	}
	out := make(Labels, 0, len(m))
	for k, v := range m {
		out = append(out, Label{Name: in.String(k), Value: in.String(v)})
	}
	slices.SortFunc(out, func(a, b Label) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Label is a single name/value pair.
type Label struct {
	Name  string
	Value string
}

// Labels is a label set sorted by name, which makes comparison and formatting
// independent of the order labels were discovered in.
type Labels []Label

// Get returns the value for name, or "" if absent.
func (ls Labels) Get(name string) string {
	i, ok := slices.BinarySearchFunc(ls, name, func(l Label, n string) int {
		return strings.Compare(l.Name, n)
	})
	if !ok {
		return ""
	}
	return ls[i].Value
}

// Has reports whether name is present.
func (ls Labels) Has(name string) bool {
	_, ok := slices.BinarySearchFunc(ls, name, func(l Label, n string) int {
		return strings.Compare(l.Name, n)
	})
	return ok
}

// Equal reports whether two label sets have identical contents.
func (ls Labels) Equal(other Labels) bool {
	return slices.Equal(ls, other)
}

// Map converts back to a plain map, for JSON encoding.
func (ls Labels) Map() map[string]string {
	m := make(map[string]string, len(ls))
	for _, l := range ls {
		m[l.Name] = l.Value
	}
	return m
}

// String renders the set as `name="value", ...` in sorted order.
func (ls Labels) String() string {
	var b strings.Builder
	for i, l := range ls {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(l.Name)
		b.WriteString(`="`)
		b.WriteString(l.Value)
		b.WriteString(`"`)
	}
	return b.String()
}

// Bytes returns the total length of every interned string, which is the
// label memory the whole fleet shares.
func (in *Interner) Bytes() int {
	n := 0
	for i := range in.shards {
		sh := &in.shards[i]
		sh.mu.RLock()
		for s := range sh.m {
			n += len(s)
		}
		sh.mu.RUnlock()
	}
	return n
}
