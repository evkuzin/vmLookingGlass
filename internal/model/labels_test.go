package model

import (
	"testing"
	"unsafe"
)

// A label is a pair of indices into the interner, not a pair of string
// headers. Every target holds its own copy of every label, so this is the
// difference between a few hundred megabytes and a few gigabytes.
func TestLabelIsCompact(t *testing.T) {
	if got := unsafe.Sizeof(Label{}); got != 8 {
		t.Errorf("Label is %d bytes, want 8 (two uint32 indices)", got)
	}
}

func TestLabelsResolveThroughInterner(t *testing.T) {
	in := NewInterner()
	ls := in.Labels(map[string]string{"job": "node", "env": "prod"})

	if got := ls.Get(in, "job"); got != "node" {
		t.Errorf("Get(job) = %q, want node", got)
	}
	if got := ls.Get(in, "env"); got != "prod" {
		t.Errorf("Get(env) = %q, want prod", got)
	}
	if got := ls.Get(in, "missing"); got != "" {
		t.Errorf("Get(missing) = %q, want empty", got)
	}
	if !ls.Has(in, "job") {
		t.Error("Has(job) = false")
	}
	if ls.Has(in, "missing") {
		t.Error("Has(missing) = true")
	}
}

// Sorting by name has to hold after the switch to indices, since ordering
// and lookup both depend on it.
func TestLabelsSortedByName(t *testing.T) {
	in := NewInterner()
	ls := in.Labels(map[string]string{"zebra": "1", "alpha": "2", "middle": "3"})

	want := []string{"alpha", "middle", "zebra"}
	for i, w := range want {
		if got := in.Name(ls[i]); got != w {
			t.Errorf("label %d = %q, want %q", i, got, w)
		}
	}
	if got := ls.String(in); got != `alpha="2", middle="3", zebra="1"` {
		t.Errorf("String() = %q", got)
	}
}

// Equal indices mean equal strings, so comparison stays a cheap slice compare
// rather than resolving every label.
func TestLabelsEqual(t *testing.T) {
	in := NewInterner()
	a := in.Labels(map[string]string{"job": "node", "env": "prod"})
	b := in.Labels(map[string]string{"env": "prod", "job": "node"})
	c := in.Labels(map[string]string{"job": "redis"})

	if !a.Equal(b) {
		t.Error("sets with equal content compare unequal")
	}
	if a.Equal(c) {
		t.Error("sets with different content compare equal")
	}
}

func TestLabelsMap(t *testing.T) {
	in := NewInterner()
	want := map[string]string{"job": "node", "env": "prod"}
	got := in.Labels(want).Map(in)

	if len(got) != len(want) {
		t.Fatalf("Map() has %d entries, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Map()[%q] = %q, want %q", k, got[k], v)
		}
	}
}

// The same string interned twice yields the same index, which is what makes
// deduplication work at all.
func TestInternIndexIsStable(t *testing.T) {
	in := NewInterner()
	a := in.Intern("node_exporter")
	b := in.Intern(string([]byte("node_exporter")))

	if a != b {
		t.Errorf("same string got indices %d and %d", a, b)
	}
	if got := in.Lookup(a); got != "node_exporter" {
		t.Errorf("Lookup(%d) = %q", a, got)
	}
}

// The empty string is common in label values and must not consume an index
// per occurrence.
func TestInternEmptyIndex(t *testing.T) {
	in := NewInterner()
	if idx := in.Intern(""); idx != emptyIndex {
		t.Errorf("empty string got index %d, want the reserved %d", idx, emptyIndex)
	}
	if got := in.Lookup(emptyIndex); got != "" {
		t.Errorf("Lookup(empty) = %q, want empty", got)
	}
}

// Indices are handed out under concurrency, so two goroutines interning the
// same new string must agree rather than each getting one.
func TestInternIndexConcurrent(t *testing.T) {
	in := NewInterner()
	const goroutines = 32

	idx := make([]uint32, goroutines)
	done := make(chan int, goroutines)
	for g := range goroutines {
		go func() {
			idx[g] = in.Intern("contended-value")
			done <- g
		}()
	}
	for range goroutines {
		<-done
	}
	for g := 1; g < goroutines; g++ {
		if idx[g] != idx[0] {
			t.Fatalf("goroutine %d got index %d, goroutine 0 got %d", g, idx[g], idx[0])
		}
	}
	if got := in.Lookup(idx[0]); got != "contended-value" {
		t.Errorf("Lookup = %q", got)
	}
}

// Lookup of an index that was never handed out must not panic: a corrupt or
// zero-valued Label should read as empty rather than crash a request.
func TestLookupOutOfRange(t *testing.T) {
	in := NewInterner()
	in.Intern("only")
	if got := in.Lookup(9999); got != "" {
		t.Errorf("Lookup(out of range) = %q, want empty", got)
	}
}
