package arena

import (
	"errors"
	"testing"
	"unsafe"
	"velocakv/page"
)

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
