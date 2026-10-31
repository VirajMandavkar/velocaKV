package tree

import (
	"errors"
	"strings"
	"testing"
	"velocakv/arena"
	"velocakv/page"
)

// newTestTree returns a one-slab tree that is closed when the test ends.
// Close any Worker before then, or the cleanup reports ErrWorkersOpen.
func newTestTree(t *testing.T) *Tree {
	t.Helper()
	tr, err := New(page.SlabBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tr.Close(); err != nil {
			t.Errorf("tree Close: %v", err)
		}
	})
	return tr
}

func TestNewEmptyRoot(t *testing.T) {
	tr := newTestTree(t)
	w, err := tr.NewWorker()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)

	d := desc(tr.root.Load())
	if d != 0x0000000100000001 {
		t.Fatalf("root desc = %#016x, want 0x0000000100000001", uint64(d))
	}
	p, err := load(w.aw, d)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind() != page.KindLeaf {
		t.Errorf("kind = %d, want KindLeaf", p.Kind())
	}
	if n := p.SlotCount(); n != 0 {
		t.Errorf("slots = %d, want 0", n)
	}
	if n := p.LowFenceLen(); n != 0 {
		t.Errorf("low fence len = %d, want 0", n)
	}
	if _, inf := p.HighFence(); !inf {
		t.Error("high fence is finite, want +inf")
	}
}

func TestTreeCloseRefusesOpenWorkers(t *testing.T) {
	tr, err := New(page.SlabBytes)
	if err != nil {
		t.Fatal(err)
	}
	w, err := tr.NewWorker()
	if err != nil {
		t.Fatal(err)
	}

	if err := tr.Close(); !errors.Is(err, arena.ErrWorkersOpen) {
		t.Fatalf("Close with an open worker: got %v, want ErrWorkersOpen", err)
	}

	w.Close()
	w.Close()
	if err := tr.Close(); err != nil {
		t.Fatalf("Close after worker closed: %v", err)
	}
}

func TestCheckKey(t *testing.T) {
	cases := []struct {
		name string
		key  []byte
		want error
	}{
		{"empty", nil, ErrEmptyKey},
		{"one byte", []byte("a"), nil},
		{"max", []byte(strings.Repeat("k", page.MaxKeyLen)), nil},
		{"max+1", []byte(strings.Repeat("k", page.MaxKeyLen+1)), ErrKeyTooLong},
	}
	for _, c := range cases {
		if err := checkKey(c.key); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

// Two Workers, used one after another, share one tree: each has its own
// scratch page and reservations, and checkTree's accounting counts both.
func TestTwoWorkers(t *testing.T) {
	tr := newTestTree(t)
	w1, err := tr.NewWorker()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w1.Close)
	w2, err := tr.NewWorker()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w2.Close) // second Close is a no-op if the test closes it first

	const n = 10_000
	for i := range n { // w1 builds the tree: many splits
		if err := w1.Put(key11(i), val16); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkTree(w1, 2); err != nil { // two scratch pages live
		t.Fatalf("checkTree(w1, 2): %v", err)
	}

	for i := range n { // w2 sees everything w1 wrote
		mustGet(t, w2, key11(i), val16)
	}
	for i := n; i < 2*n; i++ { // w2 splits with its own scratch and reservation
		if err := w2.Put(key11(i), val16); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range []*Worker{w1, w2} { // either worker sees one consistent tree
		if err := checkTree(w, 2); err != nil {
			t.Fatalf("checkTree with 2 workers: %v", err)
		}
	}
	if keys, _ := scanAll(t, w1, nil, nil); len(keys) != 2*n {
		t.Fatalf("w1 scans %d keys, want %d", len(keys), 2*n)
	}

	w2.Close() // its scratch page is freed: one worker left
	if err := checkTree(w1, 1); err != nil {
		t.Fatalf("checkTree(w1, 1) after w2.Close: %v", err)
	}
}
