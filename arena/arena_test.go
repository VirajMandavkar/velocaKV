package arena

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
	"velocakv/page"
)

func wantLive(t *testing.T, a *Arena, want int) {
	t.Helper()
	if got := a.LivePages(); got != want {
		t.Fatalf("LivePages = %d, want %d", got, want)
	}
}

// growTo calls grow once and fails the test unless it returns want.
func growTo(t *testing.T, a *Arena, want int) {
	t.Helper()
	got, err := a.grow()
	if err != nil || got != want {
		t.Fatalf("grow() = (%d, %v), want (%d, nil)", got, err, want)
	}
}

// growErr calls grow once and fails the test unless it returns target.
func growErr(t *testing.T, a *Arena, target error) {
	t.Helper()
	if _, err := a.grow(); !errors.Is(err, target) {
		t.Fatalf("grow() error = %v, want %v", err, target)
	}
}

func TestArenaLifecycle(t *testing.T) {
	P := page.PagesPerSlab

	// A budget smaller than one slab is rejected.
	if _, err := New(1 << 20); err == nil {
		t.Fatal("New(1 MiB): got nil error, want non-nil")
	}

	// 130 MiB allows exactly 2 slabs.
	a, err := New(130 << 20)
	if err != nil {
		t.Fatalf("New(130 MiB): %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if a.resolve(0) != nil {
		t.Fatal("resolve(0) before grow: got a page, want nil")
	}

	growTo(t, a, 0)
	growTo(t, a, 1)
	growErr(t, a, ErrArenaFull)

	// A page in slab 1 resolves, is 8-aligned, and aliases the slab bytes.
	p := a.resolve(uint32(P + 3))
	if p == nil {
		t.Fatalf("resolve(%d): got nil, want a page", P+3)
	}
	if addr := uintptr(unsafe.Pointer(p)); addr&7 != 0 {
		t.Fatalf("resolve(%d): address %#x is not 8-aligned", P+3, addr)
	}
	p[0] = 0xAB
	if got := (*a.slabs.Load())[1][3*page.PageSize]; got != 0xAB {
		t.Fatalf("slab 1 byte %d = %#x, want 0xab", 3*page.PageSize, got)
	}

	if a.resolve(uint32(2*P)) != nil {
		t.Fatalf("resolve(%d): got a page, want nil (slab 2 not mapped)", 2*P)
	}

	// After Close: no growth, no pages, and a second Close is harmless.
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	growErr(t, a, ErrClosed)
	if a.resolve(0) != nil {
		t.Fatal("resolve(0) after Close: got a page, want nil")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestPopFreeSteals(t *testing.T) {
	a, err := New(64 << 20)
	if err != nil {
		t.Fatalf("New Err: got %v want nil", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if n, err := a.grow(); n != 0 || err != nil {
		t.Fatalf("grow Err: got %v want nil\n int got %d want 0", err, n)
	}
	a.shards = make([]shard, 4)
	cases := []struct {
		s    int
		want []uint32
	}{
		{0, []uint32{5, 9, 7, 0}},
		{1, []uint32{9, 7, 5, 0}},
		{2, []uint32{9, 7, 5, 0}},
		{3, []uint32{7, 5, 9, 0}},
	}
	for _, c := range cases {
		a.shards = make([]shard, 4) // fresh, empty shelves for each case
		a.pushFree(0, 5)
		a.pushFree(2, 9)
		a.pushFree(3, 7)
		for _, want := range c.want {
			if got := a.popFree(c.s); got != want {
				t.Errorf("worker %d: popFree = %d, want %d", c.s, got, want)
			}
		}
	}
}

func TestAllocFreeAndSteal(t *testing.T) {
	a, err := New(130 << 20)
	if err != nil {
		t.Fatalf("New(130 MiB): %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	a.shards = make([]shard, 2)

	alloc := func(worker int, wantID uint32, wantGen uint32) {
		t.Helper()
		id, p, err := a.alloc(worker)
		if err != nil {
			t.Fatalf("alloc(%d): %v", worker, err)
		}
		if id != wantID {
			t.Fatalf("alloc(%d) ID = %d, want %d", worker, id, wantID)
		}
		if p == nil {
			t.Fatalf("alloc(%d): got nil page, want generation %d", worker, wantGen)
		}
		if got := p.Gen(); got != wantGen {
			t.Fatalf("alloc(%d) page generation = %d, want %d", worker, got, wantGen)
		}
	}

	alloc(0, 1, 1)
	alloc(0, 2, 1)
	alloc(0, 3, 1)
	wantLive(t, a, 3)

	a.free(0, 2)
	a.free(0, 1)
	wantLive(t, a, 1)
	for _, tc := range []struct {
		id      uint32
		wantNil bool
	}{{1, true}, {2, true}, {3, false}} {
		if got := a.page(tc.id); (got == nil) != tc.wantNil {
			t.Errorf("page(%d) nil = %t, want %t", tc.id, got == nil, tc.wantNil)
		}
	}

	alloc(1, 1, 3)
	alloc(0, 2, 3)
	wantLive(t, a, 3)

	a.free(0, 3)
	wantLive(t, a, 2)
	mustPanic(t, func() { a.free(0, 3) })
}

func TestAllocUntilFull(t *testing.T) {
	a, err := New(64 << 20)
	if err != nil {
		t.Fatalf("New(64 MiB): %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	a.shards = make([]shard, 1)

	var freedID uint32
	for i := range int(page.PagesPerSlab) - 1 {
		id, _, err := a.alloc(0)
		if err != nil {
			t.Fatalf("alloc(%d): %v", i, err)
		}
		freedID = id
	}

	if _, _, err := a.alloc(0); !errors.Is(err, ErrArenaFull) {
		t.Fatalf("alloc() error = %v, want %v", err, ErrArenaFull)
	}
	wantLive(t, a, page.PagesPerSlab-1)

	a.free(0, freedID)
	if _, _, err := a.alloc(0); err != nil {
		t.Fatalf("alloc() after free: %v", err)
	}
}

func TestConcurrentAllocFreeNoDuplicateAndIncreasingGen(t *testing.T) {
	a, err := New(1 << 30)
	if err != nil {
		t.Fatalf("New(1 GiB): %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	maxID := 16 * page.PagesPerSlab
	owned := make([]atomic.Bool, maxID)
	lastGen := make([]atomic.Uint32, maxID)

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()

			s := worker % len(a.shards)
			hand := make([]uint32, 0, 16)
			for i := 0; i < 50_000; i++ {
				if len(hand) < 16 {
					id, p, err := a.alloc(s)
					if err != nil {
						t.Errorf("worker %d alloc round %d: %v", worker, i, err)
						return
					}
					if p == nil {
						t.Errorf("worker %d alloc round %d page %d: got nil page", worker, i, id)
						return
					}
					if int(id) >= maxID {
						t.Errorf("worker %d alloc round %d: page ID %d exceeds max ID %d", worker, i, id, maxID-1)
						return
					}

					gen := p.Gen()
					if gen&1 != 1 {
						t.Errorf("worker %d alloc round %d page %d: generation %d is even", worker, i, id, gen)
						return
					}
					if !owned[id].CompareAndSwap(false, true) {
						t.Errorf("worker %d alloc round %d: page %d is already owned", worker, i, id)
						return
					}
					if old := lastGen[id].Swap(gen); gen <= old {
						t.Errorf("worker %d alloc round %d page %d: generation %d did not increase from %d", worker, i, id, gen, old)
						return
					}
					hand = append(hand, id)
				} else {
					id := hand[0]
					hand = hand[1:]
					owned[id].Store(false)
					a.free(s, id)
				}
			}

			for _, id := range hand {
				owned[id].Store(false)
				a.free(s, id)
			}
		}(w)
	}
	wg.Wait()
}

func TestCloseRefusesOpenWorkers(t *testing.T) {
	a, err := New(page.SlabBytes)
	if err != nil {
		t.Fatal(err)
	}

	w, err := a.NewWorker()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); !errors.Is(err, ErrWorkersOpen) {
		t.Fatalf("Close with an open worker: got %v, want ErrWorkerOpen", err)
	}

	if _, _, err := w.Alloc(); err != nil {
		t.Fatalf("Alloc after refused Close: %v", err)
	}

	w.Close()
	if err := a.Close(); err != nil {
		t.Fatalf("Close after worker closed : %v", err)
	}
}
