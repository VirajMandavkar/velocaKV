package tree

import (
	"encoding/binary"
	"errors"
	"testing"
	"velocakv/page"
)

// newPage allocates and formats a page. "" means an infinite fence.
func newPage(t *testing.T, w *Worker, kind uint8, low, high string) (*page.Page, desc) {
	t.Helper()
	id, p, err := w.aw.Alloc()
	if err != nil {
		t.Fatal(err)
	}
	var lo, hi []byte
	if low != "" {
		lo = []byte(low)
	}
	if high != "" {
		hi = []byte(high)
	}
	if err := p.Format(kind, lo, hi); err != nil {
		t.Fatal(err)
	}
	return p, makeDesc(id, p.Gen())
}

// putRaw stores key directly into a page, bypassing descent.
func putRaw(t *testing.T, w *Worker, p *page.Page, key, val string) {
	t.Helper()
	if err := p.Insert([]byte(key[p.PrefixLen():]), []byte(val), &w.env); err != nil {
		t.Fatalf("Insert(%q): %v", key, err)
	}
}

// addSep adds separator sep → child to an inner page.
func addSep(t *testing.T, w *Worker, p *page.Page, sep string, child desc) {
	t.Helper()
	val := binary.LittleEndian.AppendUint64(nil, uint64(child))
	if err := p.Insert([]byte(sep)[p.PrefixLen():], val, &w.env); err != nil {
		t.Fatalf("addSep(%q): %v", sep, err)
	}
}

// Root ["m"] → L (< "m"), upperChild → R (>= "m").
func TestDescendRoutes(t *testing.T) {
	tr, w := newOpsWorker(t)
	L, dl := newPage(t, w, page.KindLeaf, "", "m")
	R, dr := newPage(t, w, page.KindLeaf, "m", "")
	root, droot := newPage(t, w, page.KindInner, "", "")

	addSep(t, w, root, "m", dl)
	root.SetUpperChild(uint64(dr))
	L.SetRightSib(uint64(dr))
	tr.root.Store(uint64(droot))

	putRaw(t, w, L, "a", "in-L")
	putRaw(t, w, R, "x", "in-R")

	mustGet(t, w, []byte("a"), []byte("in-L"))
	mustGet(t, w, []byte("x"), []byte("in-R"))

	// A Put through the tree lands in the right leaf.
	if err := w.Put([]byte("c"), []byte("val-c")); err != nil {
		t.Fatal(err)
	}
	if got, err := L.Get([]byte("c")[L.PrefixLen():], nil, &w.env); err != nil || string(got) != "val-c" {
		t.Fatalf("L.Get(c) = %q, %v; want \"new\" in L", got, err)
	}

	if err := w.Put([]byte("m"), []byte("edge")); err != nil { // equal to the separator: goes right
		t.Fatal(err)
	}
	if got, err := R.Get([]byte("m")[R.PrefixLen():], nil, &w.env); err != nil || string(got) != "edge" {
		t.Fatalf("R.Get(m) = %q, %v; want \"edge\" in R", got, err)
	}

	// The path holds exactly the root.
	if _, _, err := w.descend([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if w.depth != 1 || w.path[0] != droot {
		t.Fatalf("path = %v (depth %d), want [root]", w.path[:w.depth], w.depth)
	}
}

// P70 state: R exists and is linked from L, but the parent never got the
// separator. Every key routes to L; keys >= "m" must still be found in R.
func TestDescendMovesRight(t *testing.T) {
	tr, w := newOpsWorker(t)
	L, dl := newPage(t, w, page.KindLeaf, "", "m")
	R, dr := newPage(t, w, page.KindLeaf, "m", "")
	root, droot := newPage(t, w, page.KindInner, "", "")
	root.SetUpperChild(uint64(dl)) // no separators: everything goes to L
	L.SetRightSib(uint64(dr))
	tr.root.Store(uint64(droot))

	putRaw(t, w, L, "a", "in-L")
	putRaw(t, w, R, "x", "in-R")

	mustGet(t, w, []byte("a"), []byte("in-L"))
	mustGet(t, w, []byte("x"), []byte("in-R")) // root → L → "x" >= "m" → R
}

func TestDescendBrokenSiblingIsCorrupt(t *testing.T) {
	tr, w := newOpsWorker(t)
	_, dl := newPage(t, w, page.KindLeaf, "", "m") // high fence "m", rightSib 0
	root, droot := newPage(t, w, page.KindInner, "", "")
	root.SetUpperChild(uint64(dl))
	tr.root.Store(uint64(droot))

	if _, err := w.Get([]byte("x"), nil); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Get(x) = %v, want ErrCorrupt", err)
	}
}
