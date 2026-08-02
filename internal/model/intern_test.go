package model

import (
	"fmt"
	"sync"
	"testing"
	"unsafe"
)

// sameBacking reports whether two strings share a backing array, i.e. one is
// not a distinct copy of the other.
func sameBacking(a, b string) bool {
	return unsafe.StringData(a) == unsafe.StringData(b)
}

func TestInternReturnsSharedBacking(t *testing.T) {
	in := NewInterner()
	// Distinct allocations with equal contents, so pointer equality is only
	// possible if the interner actually deduplicated them.
	a := string([]byte("__meta_consul_service"))
	b := string([]byte("__meta_consul_service"))
	if sameBacking(a, b) {
		t.Fatal("test setup: inputs already share backing")
	}

	ia, ib := in.String(a), in.String(b)
	if ia != b {
		t.Fatalf("interned value = %q, want %q", ia, b)
	}
	if !sameBacking(ia, ib) {
		t.Error("interner returned distinct backing arrays for equal strings")
	}
}

func TestInternLen(t *testing.T) {
	in := NewInterner()
	for _, s := range []string{"a", "b", "a", "c", "b"} {
		in.String(s)
	}
	if got := in.Len(); got != 3 {
		t.Errorf("Len = %d, want 3", got)
	}
}

func TestInternEmptyString(t *testing.T) {
	in := NewInterner()
	if got := in.String(""); got != "" {
		t.Errorf("String(\"\") = %q, want empty", got)
	}
}

func TestInternLabels(t *testing.T) {
	in := NewInterner()
	l1 := in.Labels(map[string]string{"job": "node", "env": "prod"})
	l2 := in.Labels(map[string]string{"job": "node", "env": "prod"})

	if l1.Get("job") != "node" || l1.Get("env") != "prod" {
		t.Fatalf("labels not preserved: %v", l1)
	}
	if l1.Get("missing") != "" {
		t.Error("Get on absent key should return empty string")
	}
	// 2 names + 2 values, shared across both calls.
	if got := in.Len(); got != 4 {
		t.Errorf("Len = %d, want 4 unique strings across both label sets", got)
	}
	if !sameBacking(l1.Get("job"), l2.Get("job")) {
		t.Error("equal label values across sets do not share backing")
	}
}

func TestLabelsSortedAndEqual(t *testing.T) {
	in := NewInterner()
	// Same content, different insertion order, must compare equal.
	a := in.Labels(map[string]string{"z": "1", "a": "2", "m": "3"})
	b := in.Labels(map[string]string{"m": "3", "z": "1", "a": "2"})

	if !a.Equal(b) {
		t.Error("label sets with equal content compare unequal")
	}
	if a.String() != b.String() {
		t.Errorf("String() differs by insertion order:\n %q\n %q", a.String(), b.String())
	}
	// Sorted order makes String() and Equal() order-independent.
	want := `a="2", m="3", z="1"`
	if a.String() != want {
		t.Errorf("String() = %q, want %q", a.String(), want)
	}
}

func TestLabelsNotEqual(t *testing.T) {
	in := NewInterner()
	base := in.Labels(map[string]string{"job": "node"})
	tests := []struct {
		name string
		with map[string]string
	}{
		{"different value", map[string]string{"job": "vm"}},
		{"extra label", map[string]string{"job": "node", "env": "prod"}},
		{"empty", map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if base.Equal(in.Labels(tt.with)) {
				t.Error("expected sets to differ")
			}
		})
	}
}

func TestInternConcurrent(t *testing.T) {
	in := NewInterner()
	const goroutines, keys = 8, 100

	var wg sync.WaitGroup
	got := make([][]string, goroutines)
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := make([]string, keys)
			for i := range keys {
				out[i] = in.String(fmt.Sprintf("value-%d", i))
			}
			got[g] = out
		}()
	}
	wg.Wait()

	if in.Len() != keys {
		t.Errorf("Len = %d, want %d", in.Len(), keys)
	}
	// Every goroutine must observe the same canonical instance.
	for g := 1; g < goroutines; g++ {
		for i := range keys {
			if !sameBacking(got[0][i], got[g][i]) {
				t.Fatalf("goroutine %d key %d got a different backing array", g, i)
			}
		}
	}
}

// Interning is on the hot path of every poll, and the whole fleet interns the
// same vocabulary at once, so throughput must not collapse under concurrency.
func TestInternScalesWithConcurrency(t *testing.T) {
	const goroutines, keys = 64, 500

	in := NewInterner()
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range keys {
				in.String(fmt.Sprintf("label-value-%d", i))
			}
		}()
	}
	wg.Wait()

	if got := in.Len(); got != keys {
		t.Errorf("Len = %d, want %d; concurrent interning lost or duplicated entries", got, keys)
	}
}
