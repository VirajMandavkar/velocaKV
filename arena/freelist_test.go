package arena

import (
	"sync"
	"sync/atomic"
	"testing"
)

func mustPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic, got none")
		}
	}()
	f()
}

func TestFreeListLIFO(t *testing.T) {
	// 1. arena with one slab
	a, err := New(64 << 20)
	if err != nil {
		t.Fatalf("New Err: got %v want nil", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if n, err := a.grow(); n != 0 || err != nil {
		t.Fatalf("grow Err: got %v want nil\n int got %d want 0", err, n)
	}

	var l freeList

	// 2. Empty list pops 0.
	if got := l.pop(a); got != 0 {
		t.Fatalf("pop() = %d, want 0", got)
	}

	// 3. push 1, 2, 3
	l.push(a, 1)
	l.push(a, 2)
	l.push(a, 3)

	// 4. Pops come back 3, 2, 1.
	for _, want := range []uint32{3, 2, 1} {
		if got := l.pop(a); got != want {
			t.Fatalf("pop() = %d, want %d", got, want)
		}
	}

	// 5. Empty again.
	if got := l.pop(a); got != 0 {
		t.Fatalf("pop() = %d, want 0", got)
	}

	// 6. Three successful pops incremented the count; the top is empty.
	count, top := unpack(l.head.Load())
	if count != 3 || top != 0 {
		t.Fatalf("unpack(head) = (%d, %d), want (3, 0)", count, top)
	}

	// 7. ID 0 is reserved and cannot be pushed.
	mustPanic(t, func() { l.push(a, 0) })
}

func TestFreeListConcurrent(t *testing.T) {
	const (
		pages   = 64      // pages on the list
		workers = 8       // goroutines
		iters   = 100_000 // pop/push rounds per worker
	)
	a, err := New(64 << 20)
	if err != nil {
		t.Fatalf("New Err: got %v want nil", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if n, err := a.grow(); n != 0 || err != nil {
		t.Fatalf("grow Err: got %v want nil\n int got %d want 0", err, n)
	}

	var l freeList
	for id := uint32(1); id <= pages; id++ {
		l.push(a, id)
	}

	owned := make([]atomic.Bool, pages+1) // index = page ID

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := l.pop(a)
				if id == 0 {
					t.Errorf("free list unexpectedly empty")
					return
				}
				if !owned[id].CompareAndSwap(false, true) {
					t.Errorf("duplicate page id %d popped", id)
					return
				}
				if !owned[id].CompareAndSwap(true, false) {
					t.Errorf("failed to release page id %d", id)
					return
				}
				l.push(a, id)
			}
		}()
	}
	wg.Wait()

	seen := make([]bool, pages+1)
	count := 0
	for count <= pages {
		id := l.pop(a)
		if id == 0 {
			break
		}
		if id > pages {
			t.Fatalf("popped invalid page id %d", id)
		}
		if seen[id] {
			t.Fatalf("page id %d popped twice: the chain has a cycle", id)
		}
		seen[id] = true
		count++
	}
	if count != pages {
		t.Fatalf("popped %d ids, want %d", count, pages)
	}
	for id := uint32(1); id <= pages; id++ {
		if !seen[id] {
			t.Fatalf("missing page id %d", id)
		}
	}
}
