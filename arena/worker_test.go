package arena

import (
	"errors"
	"runtime"
	"testing"

	"velocakv/page"
)

var _ page.PageSource = (*Worker)(nil) // compile-time check

func TestWorkers(t *testing.T) {
	n := 4 * runtime.GOMAXPROCS(0)
	a, err := New(page.SlabBytes)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	workers := make([]*Worker, n)
	for i := range workers {
		w, err := a.NewWorker()
		if err != nil {
			t.Fatalf("NewWorker() #%d: %v", i, err)
		}
		workers[i] = w
		if w.slot != i {
			t.Errorf("worker #%d slot = %d, want %d", i, w.slot, i)
		}
		if want := i % len(a.shards); w.shard != want {
			t.Errorf("worker #%d shard = %d, want %d", i, w.shard, want)
		}
	}

	if _, err := a.NewWorker(); !errors.Is(err, ErrTooManyWorkers) {
		t.Fatalf("NewWorker() after filling slots: error = %v, want %v", err, ErrTooManyWorkers)
	}

	// Use slot 5 when available; a one-P runtime has only four worker slots.
	recycleIndex := 5
	if recycleIndex >= n {
		recycleIndex = n - 1
	}
	workers[recycleIndex].Close()
	recycled, err := a.NewWorker()
	if err != nil {
		t.Fatalf("NewWorker() after closing slot %d: %v", recycleIndex, err)
	}
	if recycled.slot != recycleIndex {
		t.Fatalf("recycled worker slot = %d, want %d", recycled.slot, recycleIndex)
	}

	before := len(a.freeSlots)
	recycled.Close()
	recycled.Close()
	if got := len(a.freeSlots); got != before+1 {
		t.Fatalf("free slot count after closing worker twice = %d, want %d", got, before+1)
	}

	for _, w := range workers {
		w.Close()
	}
	if got := len(a.freeSlots); got != n {
		t.Fatalf("free slot count after closing all workers = %d, want %d", got, n)
	}

	w, err := a.NewWorker()
	if err != nil {
		t.Fatalf("NewWorker() for PageSource check: %v", err)
	}
	var src page.PageSource = w
	id, p, err := src.Alloc()
	if err != nil {
		t.Fatalf("PageSource.Alloc(): %v", err)
	}
	if got := src.Page(id); got != p {
		t.Fatalf("PageSource.Page(%d) = %p, want allocated page %p", id, got, p)
	}
	src.Free(id)
	if got := src.Page(id); got != nil {
		t.Fatalf("PageSource.Page(%d) after Free = %p, want nil", id, got)
	}
	w.Close()

	if err := a.Close(); err != nil {
		t.Fatalf("Arena.Close(): %v", err)
	}
	if _, err := a.NewWorker(); !errors.Is(err, ErrClosed) {
		t.Fatalf("NewWorker() after Arena.Close(): error = %v, want %v", err, ErrClosed)
	}
}
