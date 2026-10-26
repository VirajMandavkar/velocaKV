package tree

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"velocakv/page"
)

var val16 = bytes.Repeat([]byte("v"), 16)

const perLeaf = page.Usable / (page.SlotSize + 8 + 16)

func key(i int) []byte { return fmt.Appendf(nil, "key-%04d", i) }

func TestRootSplit(t *testing.T) {
	tr, w := newOpsWorker(t)
	oldRoot := desc(tr.root.Load())

	for i := range perLeaf {
		if err := w.Put(key(i), val16); err != nil {
			t.Fatalf("Put(%q): %v", key(i), err)
		}
	}
	if desc(tr.root.Load()) != oldRoot {
		t.Fatalf("root split before the leaf was full (%d keys)", perLeaf)
	}

	if err := w.Put(key(perLeaf), val16); err != nil {
		t.Fatalf("Put that splits the root: %v", err)
	}

	root, err := load(w.aw, desc(tr.root.Load()))
	if err != nil {
		t.Fatal(err)
	}
	if root.Kind() != page.KindInner || root.SlotCount() != 1 {
		t.Fatalf("new root: kind %d, %d separators; want inner, 1", root.Kind(), root.SlotCount())
	}

	m := perLeaf / 2
	wantSep := key(m)[:commonPrefix(key(m-1), key(m))+1]
	if sep := root.KeyAt(0, nil); !bytes.Equal(sep, wantSep) {
		t.Fatalf("separator = %q, want %q", sep, wantSep)
	}

	child, _ := root.ChildAt(0)
	if desc(child) != oldRoot {
		t.Fatalf("left child = %#x, want the old root %#x", child, uint64(oldRoot))
	}
	left, _ := load(w.aw, oldRoot)
	right, err := load(w.aw, desc(root.UpperChild()))
	if err != nil {
		t.Fatal(err)
	}

	if l, r := int(left.SlotCount()), int(right.SlotCount()); l != m || r != perLeaf-m+1 {
		t.Fatalf("left %d, right %d records; want %d, %d", l, r, m, perLeaf-m+1)
	}

	if err := checkTree(w, 1); err != nil {
		t.Fatalf("checkTree after root split: %v", err)
	}
	for i := 0; i <= perLeaf; i++ {
		mustGet(t, w, key(i), val16)
	}
	if live := tr.a.LivePages(); live != 4 {
		t.Fatalf("live pages = %d, want 4", live)
	}
}

func TestRootSplitMovesOverflowStubs(t *testing.T) {
	tr, w := newOpsWorker(t)
	big := func(i int) []byte {
		v := make([]byte, 3000)
		for j := range v {
			v[j] = byte(i + j)
		}
		return v
	}

	n := 0
	for ; ; n++ {
		if err := w.Put(key(n), big(n)); err != nil {
			t.Fatalf("Put(%q): %v", key(n), err)
		}
		if root, _ := load(w.aw, desc(tr.root.Load())); root.Kind() == page.KindInner {
			break
		}
	}
	t.Logf("root split after %d overflow records ", n+1)

	if err := checkTree(w, 1); err != nil {
		t.Fatalf("checkTree: %v", err)
	}
	for _, i := range []int{0, n / 2, n} {
		got, err := w.Get(key(i), nil)
		if err != nil || !bytes.Equal(got, big(i)) {
			t.Fatalf("Get(%q): %d bytes, %v", key(i), len(got), err)
		}
	}
}

func TestInsertSepCutByPrefix(t *testing.T) {
	_, w := newOpsWorker(t)
	p, _ := newPage(t, w, page.KindInner, "key-0040", "key-0050")
	if got := string(p.Prefix()); got != "key-00" {
		t.Fatalf("prefix = %q, want key-00 ", got)
	}

	const child = desc(0x00000001_00000007)
	if err := w.insertSep(p, []byte("key-0042"), child); err != nil {
		t.Fatal(err)
	}

	if got := string(p.KeyAt(0, nil)); got != "key-0042" {
		t.Fatalf("KeyAt(0) = %q, want %q", got, "key-0042")
	}
	if got, ok := p.ChildAt(0); !ok || desc(got) != child {
		t.Fatalf("ChildAt(0) = %#x, false; want %#x", got, uint64(child))
	}
	if err := p.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}

	if err := w.insertSep(p, []byte("key"), child); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("short key: got %v, want ErrCorupt", err)
	}
}
