package tree

import (
	"bytes"
	"errors"
	"testing"
	"velocakv/page"
)

func newOpsWorker(t *testing.T) (*Tree, *Worker) {
	t.Helper()
	tr := newTestTree(t)
	w, err := tr.NewWorker()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close) // registered after the tree's cleanup, so it runs first
	return tr, w
}

// rootSlots returns the root leaf's slot count.
func rootSlots(t *testing.T, tr *Tree, w *Worker) int {
	t.Helper()
	p, err := load(w.aw, desc(tr.root.Load()))
	if err != nil {
		t.Fatal(err)
	}
	return int(p.SlotCount())
}

func mustGet(t *testing.T, w *Worker, key, want []byte) {
	t.Helper()
	got, err := w.Get(key, nil)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Get(%q) = %q, want %q", key, got, want)
	}
}

func probeIDs(t *testing.T, w *Worker, n int) []uint32 {
	t.Helper()
	ids := make([]uint32, n)
	for i := range ids {
		id, _, err := w.aw.Alloc()
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	for _, id := range ids {
		w.aw.Free(id)
	}
	return ids
}

func TestOpsOverflowPages(t *testing.T) {
	_, w := newOpsWorker(t)

	const firstFresh = 3 // 1 = root, 2 = scratch
	val := bytes.Repeat([]byte("x"), 20000)
	wantPages := (len(val) + page.Usable - 1) / page.Usable

	if err := w.Put([]byte("big"), val); err != nil {
		t.Fatalf("Put: %v", err)
	}
	mustGet(t, w, []byte("big"), val)

	probe := probeIDs(t, w, 1)[0]
	if got := int(probe) - firstFresh; got != wantPages {
		t.Fatalf("overflow pages used = %d, want %d", got, wantPages)
	}

	// Delete must free the whole chain: n+1 allocations now need no fresh ID.
	if err := w.Delete([]byte("big")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, id := range probeIDs(t, w, wantPages+1) {
		if id > probe {
			t.Fatalf("got fresh page %d after Delete: overflow chain leaked", id)
		}
	}
}

func TestOpsPutGetUpdateDelete(t *testing.T) {
	tr, w := newOpsWorker(t)

	for _, k := range []string{"a", "b", "c"} {
		if err := w.Put([]byte(k), []byte("val_"+k)); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}
	for _, k := range []string{"a", "b", "c"} {
		mustGet(t, w, []byte(k), []byte("val_"+k))
	}

	if err := w.Put([]byte("b"), []byte("updated")); err != nil {
		t.Fatalf("update Put: %v", err)
	}

	mustGet(t, w, []byte("b"), []byte("updated"))
	if n := rootSlots(t, tr, w); n != 3 {
		t.Fatalf("SlotCount after update = %d, want 3", n)
	}

	if err := w.Delete([]byte("b")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := w.Get([]byte("b"), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: got %v, want ErrNotFound", err)
	}
	if err := w.Delete([]byte("b")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete: got %v, want ErrNotFound", err)
	}
	if _, err := w.Get([]byte("b"), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after second Delete: got %v, want ErrNotFound", err)
	}

	mustGet(t, w, []byte("a"), []byte("val_a"))
	mustGet(t, w, []byte("c"), []byte("val_c"))
	if n := rootSlots(t, tr, w); n != 2 {
		t.Fatalf("SlotCount after Delete = %d, want 2", n)
	}
}

func TestOpsLargeValue(t *testing.T) {
	_, w := newOpsWorker(t)

	val := bytes.Repeat([]byte("x"), 3000) // > MaxInlineRecord on both builds: goes to overflow
	if err := w.Put([]byte("overflow"), val); err != nil {
		t.Fatalf("Put large value: %v", err)
	}
	got, err := w.Get([]byte("overflow"), nil)
	if err != nil {
		t.Fatalf("Get large value: %v", err)
	}
	if !bytes.Equal(got, val) {
		t.Fatalf("Get large value: %d bytes back, want %d", len(got), len(val))
	}
}

func TestOpsGetAllocs(t *testing.T) {
	_, w := newOpsWorker(t)

	key, val := []byte("alloc"), []byte("value")
	if err := w.Put(key, val); err != nil {
		t.Fatal(err)
	}

	dst := make([]byte, 0, len(val))
	allocs := testing.AllocsPerRun(100, func() {
		got, err := w.Get(key, dst[:0])
		if err != nil || !bytes.Equal(got, val) {
			t.Fatalf("Get = %q, %v; want %q", got, err, val)
		}
	})
	if allocs != 0 {
		t.Fatalf("Get allocated %v times per call, want 0", allocs)
	}
}
