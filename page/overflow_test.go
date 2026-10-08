package page

import (
	"bytes"
	"fmt"
	"testing"
)

func TestStub(t *testing.T) {
	b := make([]byte, 8)
	putStub(b, 901, 20000)

	if !bytes.Equal(b, []byte{0x85, 0x03, 0x00, 0x00, 0x20, 0x4E, 0x00, 0x00}) {
		t.Fatalf("stub = % x, want 85 03 00 00 20 4e 00 00", b)
	}

	a, c := readStub(b)
	if a != 901 || c != 20000 {
		t.Fatalf("got (%d, %d) want (901, 20000)", a, c)
	}
}

func TestChainRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		n    int
	}{
		{name: "smallest chain", n: 1},
		{name: "exactly fills one page", n: Usable},
		{name: "spills into second page", n: Usable + 1},
		{name: "running example", n: 20000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := newMemPages()
			val := make([]byte, tt.n)
			for i := range val {
				val[i] = byte(i % 251)
			}

			head, err := writeChain(src, val)
			if err != nil {
				t.Fatalf("writeChain: %v", err)
			}
			pages := overflowPages(tt.n)
			if got := src.live(); got != pages {
				t.Fatalf("live pages after write = %d, want %d", got, pages)
			}

			got, ok := readChain(src, head, uint32(tt.n), nil)
			if !ok {
				t.Fatal("readChain failed")
			}
			if !bytes.Equal(got, val) {
				t.Fatal("readChain returned different bytes")
			}

			freeChain(src, head, pages)
			if got := src.live(); got != 0 {
				t.Fatalf("live pages after free = %d, want 0", got)
			}
		})
	}
}

func TestWriteChainAllocFails(t *testing.T) {
	val := make([]byte, 20000)
	pages := overflowPages(len(val)) // 3 on 8 KiB, 5 on 4 KiB
	for fail := 0; fail < pages; fail++ {
		t.Run(fmt.Sprint("fail after ", fail), func(t *testing.T) {
			src := newMemPages()
			src.failAfter = fail
			head, err := writeChain(src, val)
			if err == nil {
				t.Fatal("writeChain succeeded, want an allocation error")
			}
			if head != 0 {
				t.Errorf("head = %d, want 0", head)
			}
			if got := src.live(); got != 0 {
				t.Errorf("leaked %d pages", got)
			}
		})
	}
}

// twoPageChain writes a value one byte longer than a page: IDs 1 → 2.
func twoPageChain(t *testing.T) (*memPages, uint32, []byte) {
	t.Helper()
	src := newMemPages()
	val := make([]byte, Usable+1)
	for i := range val {
		val[i] = byte(i % 251)
	}
	head, err := writeChain(src, val)
	if err != nil {
		t.Fatalf("writeChain: %v", err)
	}
	return src, head, val
}

func TestReadChainBroken(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(src *memPages, head uint32) (totalLen uint32)
	}{
		{"missing page", func(src *memPages, head uint32) uint32 {
			src.Free(2)
			return Usable + 1
		}},
		{"link points at a leaf", func(src *memPages, head uint32) uint32 {
			src.Page(2).Init(KindLeaf)
			return Usable + 1
		}},
		{"chain longer than totalLen", func(src *memPages, head uint32) uint32 {
			return Usable // value ends on page 1, but page 1 still links to page 2
		}},
		{"totalLen longer than chain", func(src *memPages, head uint32) uint32 {
			return 2*Usable + 1 // needs more bytes than the 2 pages hold → reaches link 0
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, head, _ := twoPageChain(t)
			total := tt.break_(src, head)

			dst := []byte("prefix")
			got, ok := readChain(src, head, total, dst)
			if ok {
				t.Fatal("readChain ok = true, want false")
			}
			if string(got) != "prefix" {
				t.Errorf("dst = %q, want %q (no partial value)", got, "prefix")
			}
		})
	}
}

func TestUpdateOverflow(t *testing.T) {
	tests := []struct {
		name     string
		old, new []byte
	}{
		{"inline to inline", patterned(100, 0), patterned(50, 1)},
		{"inline to overflow", patterned(100, 0), patterned(20_000, 1)},
		{"overflow to inline", patterned(20_000, 0), patterned(5, 1)},
		{"overflow to overflow", patterned(20_000, 0), patterned(30_000, 1)},
		{"small inline to overflow (carves)", patterned(3, 0), patterned(20_000, 1)},
		{"overflow to bigger inline (carves)", patterned(20_000, 0), patterned(100, 1)},
	}
	key := []byte("k")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := newMemPages()
			env := &Env{Scratch: newTestPage(), Pages: src}
			p := newLeaf(t)
			wantErr(t, "Insert", p.Insert(key, tt.old, env), nil)

			wantErr(t, "Update", p.Update(key, tt.new, env), nil)
			mustCheck(t, p)

			got, err := p.Get(key, nil, env)
			wantErr(t, "Get", err, nil)
			if !bytes.Equal(got, tt.new) {
				t.Errorf("Get returned %d bytes, not the new value", len(got))
			}

			newOvf := len(key)+len(tt.new) > MaxInlineRecord
			if got := p.slotAt(0).ovf(); got != newOvf {
				t.Errorf("slot OVF = %t, want %t", got, newOvf)
			}
			wantLive := 0
			if newOvf {
				wantLive = overflowPages(len(tt.new))
			}
			if got := src.live(); got != wantLive {
				t.Errorf("live pages = %d, want %d (old chain freed, new chain kept)", got, wantLive)
			}
		})
	}

	t.Run("allocation failure keeps the old value", func(t *testing.T) {
		src := newMemPages()
		env := &Env{Scratch: newTestPage(), Pages: src}
		p := newLeaf(t)
		oldVal := patterned(20_000, 0)
		wantErr(t, "Insert", p.Insert(key, oldVal, env), nil)

		src.failAfter = len(src.pages) // allow exactly one more page, then fail
		if err := p.Update(key, patterned(30_000, 1), env); err == nil {
			t.Fatal("Update succeeded, want an allocation error")
		}
		got, err := p.Get(key, nil, env)
		wantErr(t, "Get", err, nil)
		if !bytes.Equal(got, oldVal) {
			t.Errorf("old value lost after a failed Update")
		}
		if got := src.live(); got != overflowPages(len(oldVal)) {
			t.Errorf("live pages = %d, want %d (old chain only)", got, overflowPages(len(oldVal)))
		}
		mustCheck(t, p)
	})

	t.Run("no page source to free the old chain", func(t *testing.T) {
		src := newMemPages()
		env := &Env{Scratch: newTestPage(), Pages: src}
		p := newLeaf(t)
		oldVal := patterned(20_000, 0)
		wantErr(t, "Insert", p.Insert(key, oldVal, env), nil)

		wantErr(t, "Update", p.Update(key, []byte("tiny"), &Env{Scratch: newTestPage()}), ErrNoPages)
		got, err := p.Get(key, nil, env)
		wantErr(t, "Get", err, nil)
		if !bytes.Equal(got, oldVal) {
			t.Errorf("old value changed after a rejected Update")
		}
		mustCheck(t, p)
	})
}
