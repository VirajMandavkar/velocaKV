package tree

import (
	"errors"
	"fmt"
	"testing"

	"velocakv/page"
)

func TestCheckTreeFresh(t *testing.T) {
	_, w := newOpsWorker(t)
	if err := checkTree(w, 1); err != nil {
		t.Fatalf("checkTree on fresh tree: %v", err)
	}
}

func TestCheckTreeAfterOperations(t *testing.T) {
	_, w := newOpsWorker(t)
	check := func(step string) {
		t.Helper()
		if err := checkTree(w, 1); err != nil {
			t.Fatalf("checkTree after %s: %v", step, err)
		}
	}

	check("setup")
	for i := range 50 {
		key := fmt.Appendf(nil, "key-%02d", i)
		if err := w.Put(key, []byte("small value")); err != nil {
			t.Fatalf("Put(%q): %v", key, err)
		}
		check(fmt.Sprintf("Put(%q)", key))
	}

	if err := w.Put([]byte("large"), make([]byte, 20000)); err != nil {
		t.Fatalf("Put large value: %v", err)
	}
	check("Put large value")

	if err := w.Put([]byte("large"), []byte("now small")); err != nil {
		t.Fatalf("shrink large value: %v", err)
	}
	check("shrink large value") // old chain must be freed

	for i := range 10 {
		key := fmt.Appendf(nil, "key-%02d", i)
		if err := w.Delete(key); err != nil {
			t.Fatalf("Delete(%q): %v", key, err)
		}
		check(fmt.Sprintf("Delete(%q)", key))
	}
}

func TestCheckTreeDetectsLeak(t *testing.T) {
	_, w := newOpsWorker(t)

	if _, _, err := w.aw.Alloc(); err != nil { // allocated, linked from nowhere
		t.Fatalf("Alloc: %v", err)
	}
	err := checkTree(w, 1)
	if !errors.Is(err, errPageCount) {
		t.Fatalf("checkTree = %v, want errPageCount", err)
	}
	t.Log(err) // tree+overflow=1 scratch=1 live=3
}

func TestCheckTreeDetectsScratchInTree(t *testing.T) {
	tr, w := newOpsWorker(t)

	// Simulate a split bug that publishes the scratch page as a tree node.
	// Format it first so p.Check passes and the duplicate check is what fires.
	sp := w.env.Scratch
	if err := sp.Format(page.KindLeaf, nil, nil); err != nil {
		t.Fatal(err)
	}
	tr.root.Store(uint64(makeDesc(w.scratchID, sp.Gen())))

	if err := checkTree(w, 1); !errors.Is(err, errDoubleRef) {
		t.Fatalf("checkTree = %v, want errDoubleRef", err)
	}
}

func TestCheckTreeDetectsCorruptRoot(t *testing.T) {
	_, w := newOpsWorker(t)

	p, err := load(w.aw, desc(w.t.root.Load()))
	if err != nil {
		t.Fatalf("load root: %v", err)
	}
	p.SetHeapStart(0) // heap now overlaps the header

	if err := checkTree(w, 1); !errors.Is(err, page.ErrPageInvariant) {
		t.Fatalf("checkTree = %v, want page.ErrPageInvariant", err)
	}
}

// buildTwoLeaves makes root ["m"] → L, upperChild R, with L.rightSib = R,
// and frees the root New created. Returns L, R and the new root's desc.
func buildTwoLeaves(t *testing.T, tr *Tree, w *Worker) (L, R *page.Page) {
	t.Helper()
	w.aw.Free(desc(tr.root.Load()).id())
	L, dl := newPage(t, w, page.KindLeaf, "", "m")
	R, dr := newPage(t, w, page.KindLeaf, "m", "")
	root, droot := newPage(t, w, page.KindInner, "", "")
	addSep(t, w, root, "m", dl)
	root.SetUpperChild(uint64(dr))
	L.SetRightSib(uint64(dr))
	tr.root.Store(uint64(droot))
	return L, R
}

func TestCheckTreeTwoLevels(t *testing.T) {
	tr, w := newOpsWorker(t)
	buildTwoLeaves(t, tr, w)
	if err := checkTree(w, 1); err != nil {
		t.Fatalf("valid 2-level tree: %v", err)
	}
}

func TestCheckTreeWrongFence(t *testing.T) {
	tr, w := newOpsWorker(t)
	w.aw.Free(desc(tr.root.Load()).id())
	L, dl := newPage(t, w, page.KindLeaf, "", "k") // parent will say "m"
	_, dr := newPage(t, w, page.KindLeaf, "m", "")
	root, droot := newPage(t, w, page.KindInner, "", "")
	addSep(t, w, root, "m", dl)
	root.SetUpperChild(uint64(dr))
	L.SetRightSib(uint64(dr))
	tr.root.Store(uint64(droot))

	if err := checkTree(w, 1); !errors.Is(err, errShape) {
		t.Fatalf("got %v, want errShape (L's high fence k ≠ separator m)", err)
	}
}

func TestCheckTreeBrokenSibling(t *testing.T) {
	tr, w := newOpsWorker(t)
	L, _ := buildTwoLeaves(t, tr, w)
	L.SetRightSib(0) // the chain is cut
	if err := checkTree(w, 1); !errors.Is(err, errShape) {
		t.Fatalf("got %v, want errShape", err)
	}
}
