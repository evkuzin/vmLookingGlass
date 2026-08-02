// Package model holds the aggregated target types and the label interner.
package model

import (
	"hash/maphash"
	"math"
	"slices"
	"strings"
	"sync"
)

// internShards is the number of independent maps the interner is split
// across. Every agent interns the whole fleet vocabulary on every poll, so a
// single lock serializes the hot path; spreading keys over many locks keeps
// throughput rising with concurrency instead of collapsing.
const internShards = 64

// emptyIndex is the index of the empty string, stored once at position zero
// so an absent label value costs nothing and a zero-valued Label reads as
// empty rather than as whatever happens to be first.
const emptyIndex uint32 = 0

type internShard struct {
	mu sync.RWMutex
	m  map[string]uint32
	// Pad to a cache line so adjacent shards' locks do not share one, which
	// would reintroduce contention through false sharing.
	_ [40]byte
}

// Interner assigns each distinct label string a small integer.
//
// Scrape target metadata repeats heavily: a fleet-wide poll contains millions
// of label pairs drawn from a far smaller vocabulary. Storing an index rather
// than a string header shrinks a label from 32 bytes to 8, which matters
// because every target holds its own copy of every label it carries while the
// text itself is stored once.
//
// Entries are never evicted. The distinct-string set is bounded by the label
// vocabulary of the monitored fleet, not by target count, so it converges.
type Interner struct {
	shards [internShards]internShard
	seed   maphash.Seed

	// mu guards strs, the index-to-string table. It is separate from the
	// shard locks because a lookup only reads the table while an insert
	// appends to it.
	mu   sync.RWMutex
	strs []string
}

func NewInterner() *Interner {
	in := &Interner{
		seed: maphash.MakeSeed(),
		// Position zero is the empty string, so emptyIndex is valid from the
		// start and no caller has to special-case an unset label.
		strs: []string{""},
	}
	for i := range in.shards {
		in.shards[i].m = make(map[string]uint32)
	}
	return in
}

func (in *Interner) shard(s string) *internShard {
	return &in.shards[maphash.String(in.seed, s)%internShards]
}

// Intern returns the index of s, assigning one if the string is new.
func (in *Interner) Intern(s string) uint32 {
	if s == "" {
		return emptyIndex
	}
	sh := in.shard(s)

	sh.mu.RLock()
	idx, ok := sh.m[s]
	sh.mu.RUnlock()
	if ok {
		return idx
	}

	sh.mu.Lock()
	defer sh.mu.Unlock()
	// Another goroutine may have interned it between the two locks.
	if idx, ok := sh.m[s]; ok {
		return idx
	}

	in.mu.Lock()
	// The vocabulary is bounded by the fleet's distinct label strings, orders
	// of magnitude below this, but wrapping would silently alias every label
	// that followed.
	if len(in.strs) > math.MaxUint32 {
		in.mu.Unlock()
		panic("model: interner exhausted its index space")
	}
	idx = uint32(len(in.strs)) //nolint:gosec // bounded by the check above
	in.strs = append(in.strs, s)
	in.mu.Unlock()

	sh.m[s] = idx
	return idx
}

// Lookup returns the string for an index, or "" if the index was never handed
// out. An unknown index reads as empty rather than panicking, so a corrupt
// label cannot take down a request.
func (in *Interner) Lookup(idx uint32) string {
	in.mu.RLock()
	defer in.mu.RUnlock()
	if int(idx) >= len(in.strs) {
		return ""
	}
	return in.strs[idx]
}

// String returns the canonical instance of s. It exists for callers that want
// deduplicated text rather than an index.
func (in *Interner) String(s string) string {
	return in.Lookup(in.Intern(s))
}

// Name returns a label's name.
func (in *Interner) Name(l Label) string { return in.Lookup(l.Name) }

// Value returns a label's value.
func (in *Interner) Value(l Label) string { return in.Lookup(l.Value) }

// Len returns the number of distinct interned strings, excluding the empty
// string held at position zero.
func (in *Interner) Len() int {
	in.mu.RLock()
	defer in.mu.RUnlock()
	return len(in.strs) - 1
}

// Bytes returns the total length of every interned string, which is the label
// text the whole fleet shares.
func (in *Interner) Bytes() int {
	in.mu.RLock()
	defer in.mu.RUnlock()
	n := 0
	for _, s := range in.strs {
		n += len(s)
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
		out = append(out, Label{Name: in.Intern(k), Value: in.Intern(v)})
	}
	// Sorted by name so lookup can binary search and so two sets with the
	// same content compare equal regardless of discovery order.
	slices.SortFunc(out, func(a, b Label) int {
		return strings.Compare(in.Lookup(a.Name), in.Lookup(b.Name))
	})
	return out
}

// Label is one name/value pair, held as indices into the interner that
// produced it. Resolving a label against a different interner is meaningless.
type Label struct {
	Name  uint32
	Value uint32
}

// Labels is a label set sorted by name.
type Labels []Label

// Get returns the value for name, or "" if absent.
func (ls Labels) Get(in *Interner, name string) string {
	i, ok := ls.find(in, name)
	if !ok {
		return ""
	}
	return in.Lookup(ls[i].Value)
}

// Has reports whether name is present.
func (ls Labels) Has(in *Interner, name string) bool {
	_, ok := ls.find(in, name)
	return ok
}

func (ls Labels) find(in *Interner, name string) (int, bool) {
	return slices.BinarySearchFunc(ls, name, func(l Label, n string) int {
		return strings.Compare(in.Lookup(l.Name), n)
	})
}

// Equal reports whether two label sets have identical contents. Equal indices
// mean equal strings, so this stays a plain slice comparison.
func (ls Labels) Equal(other Labels) bool {
	return slices.Equal(ls, other)
}

// Map converts back to a plain map, for JSON encoding.
func (ls Labels) Map(in *Interner) map[string]string {
	m := make(map[string]string, len(ls))
	for _, l := range ls {
		m[in.Lookup(l.Name)] = in.Lookup(l.Value)
	}
	return m
}

// String renders the set as `name="value", ...` in sorted order.
func (ls Labels) String(in *Interner) string {
	var b strings.Builder
	for i, l := range ls {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(in.Lookup(l.Name))
		b.WriteString(`="`)
		b.WriteString(in.Lookup(l.Value))
		b.WriteString(`"`)
	}
	return b.String()
}
