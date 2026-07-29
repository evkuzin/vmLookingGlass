// Package model holds the aggregated target types and the label interner.
package model

import (
	"slices"
	"strings"
	"sync"
)

// Interner deduplicates label strings across agents.
//
// Scrape target metadata repeats heavily: a fleet-wide poll contains hundreds
// of thousands of label pairs drawn from only a few thousand distinct strings,
// so canonicalising them collapses label memory by two orders of magnitude.
//
// Entries are never evicted. The distinct-string set is bounded by the label
// vocabulary of the monitored fleet, not by target count, so it converges.
type Interner struct {
	mu sync.RWMutex
	m  map[string]string
}

func NewInterner() *Interner {
	return &Interner{m: make(map[string]string)}
}

// String returns the canonical instance of s, storing it if not yet seen.
func (in *Interner) String(s string) string {
	if s == "" {
		return ""
	}
	in.mu.RLock()
	got, ok := in.m[s]
	in.mu.RUnlock()
	if ok {
		return got
	}

	in.mu.Lock()
	defer in.mu.Unlock()
	// Another goroutine may have stored it between the two locks.
	if got, ok := in.m[s]; ok {
		return got
	}
	in.m[s] = s
	return s
}

// Len returns the number of distinct interned strings.
func (in *Interner) Len() int {
	in.mu.RLock()
	defer in.mu.RUnlock()
	return len(in.m)
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
