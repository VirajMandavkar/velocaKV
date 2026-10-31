package arena

import (
	"runtime"
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
	for range workers {
		go func() {
			defer wg.Done()
			for range iters {
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

const benchPages = 1024

// newBenchArena returns an arena with one mapped slab.
func newBenchArena(b *testing.B) *Arena {
	b.Helper()
	a, err := New(64 << 20)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = a.Close() })
	if _, err := a.grow(); err != nil {
		b.Fatal(err)
	}
	return a
}

func BenchmarkFreeListSingle(b *testing.B) {
	a := newBenchArena(b)
	var l freeList
	for id := uint32(1); id <= benchPages; id++ {
		l.push(a, id)
	}

	b.ReportAllocs()
	b.ResetTimer() // don't count the setup above
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := l.pop(a)
			if id == 0 {
				b.Fatal("free list unexpectedly empty")
			}
			l.push(a, id)
		}
	})
}

func BenchmarkFreeListSharded(b *testing.B) {
	a := newBenchArena(b)
	a.shards = make([]shard, runtime.GOMAXPROCS(0)) // match this run's -cpu value
	for id := uint32(1); id <= benchPages; id++ {
		a.pushFree(int(id)%len(a.shards), id)
	}

	var next atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		s := int(next.Add(1)-1) % len(a.shards)
		for pb.Next() {
			id := a.popFree(s)
			if id == 0 {
				b.Fatal("free list unexpectedly empty")
			}
			a.pushFree(s, id)
		}
	})
}
