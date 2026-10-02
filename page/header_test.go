package page

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

// newTestPage returns a zeroed page that is 8-byte aligned by guarantee
// (the first word of an allocated []uint64 array is 64-bit aligned).
func newTestPage() *Page {
	return (*Page)(unsafe.Pointer(new([PageSize / 8]uint64)))
}

// field describes one header field: where the plan puts it and how to reach it.
type field struct {
	name string
	off  int // byte offset in the plan's header map
	size int // 1, 2 or 4 bytes
	get  func(*Page) uint64
	set  func(*Page, uint64)
}

var fields = []field{
	{"gen", 8, 4, func(p *Page) uint64 { return uint64(p.Gen()) }, func(p *Page, v uint64) { p.SetGen(uint32(v)) }},
	{"kind", 12, 1, func(p *Page) uint64 { return uint64(p.Kind()) }, func(p *Page, v uint64) { p.SetKind(uint8(v)) }},
	{"flags", 13, 1, func(p *Page) uint64 { return uint64(p.Flags()) }, func(p *Page, v uint64) { p.SetFlags(uint8(v)) }},
	{"slotCount", 14, 2, func(p *Page) uint64 { return uint64(p.SlotCount()) }, func(p *Page, v uint64) { p.SetSlotCount(uint16(v)) }},
	{"heapStart", 40, 2, func(p *Page) uint64 { return uint64(p.HeapStart()) }, func(p *Page, v uint64) { p.SetHeapStart(uint16(v)) }},
	{"deadBytes", 42, 2, func(p *Page) uint64 { return uint64(p.DeadBytes()) }, func(p *Page, v uint64) { p.SetDeadBytes(uint16(v)) }},
	{"lowFenceOff", 44, 2, func(p *Page) uint64 { return uint64(p.LowFenceOff()) }, func(p *Page, v uint64) { p.SetLowFenceOff(uint16(v)) }},
	{"lowFenceLen", 46, 2, func(p *Page) uint64 { return uint64(p.LowFenceLen()) }, func(p *Page, v uint64) { p.SetLowFenceLen(uint16(v)) }},
	{"highFenceOff", 48, 2, func(p *Page) uint64 { return uint64(p.HighFenceOff()) }, func(p *Page, v uint64) { p.SetHighFenceOff(uint16(v)) }},
	{"highFenceLen", 50, 2, func(p *Page) uint64 { return uint64(p.HighFenceLen()) }, func(p *Page, v uint64) { p.SetHighFenceLen(uint16(v)) }},
	{"prefixLen", 52, 2, func(p *Page) uint64 { return uint64(p.PrefixLen()) }, func(p *Page, v uint64) { p.SetPrefixLen(uint16(v)) }},
}

// maxFor returns the largest value a field of this many bytes can hold.
func maxFor(size int) uint64 { return 1<<(8*size) - 1 }

// rawRead reads a field the plain way, straight from the plan's byte offset.
func rawRead(p *Page, f field) uint64 {
	switch f.size {
	case 1:
		return uint64(p[f.off])
	case 2:
		return uint64(binary.LittleEndian.Uint16(p[f.off:]))
	default:
		return uint64(binary.LittleEndian.Uint32(p[f.off:]))
	}
}

// Test 1: SetX(v) then X() returns v, at 0, 1 and the max value.
func TestHeaderRoundTrip(t *testing.T) {
	for _, f := range fields {
		for _, v := range []uint64{0, 1, maxFor(f.size)} {
			p := newTestPage()
			f.set(p, v)
			if got := f.get(p); got != v {
				t.Errorf("%s: set %d, got %d", f.name, v, got)
			}
		}
	}
}

// Test 2: setting one field never changes any other field.
func TestHeaderNeighboursUntouched(t *testing.T) {
	for _, target := range fields {
		p := newTestPage()
		for _, f := range fields { // every field starts at its max: all bits 1
			f.set(p, maxFor(f.size))
		}
		target.set(p, 0) // clear only the target
		for _, f := range fields {
			want := maxFor(f.size)
			if f.name == target.name {
				want = 0
			}
			if got := f.get(p); got != want {
				t.Errorf("after clearing %s: %s = %d, want %d", target.name, f.name, got, want)
			}
		}
	}
}

// Test 3: each setter writes exactly the bytes the plan says.
func TestHeaderExactBytes(t *testing.T) {
	for _, f := range fields {
		p := newTestPage()
		v := maxFor(f.size) - 1 // not all-ones, so a byte-order mix-up shows
		f.set(p, v)
		if got := rawRead(p, f); got != v {
			t.Errorf("%s: bytes at %d hold %d, want %d", f.name, f.off, got, v)
		}
	}
}

// Test 4: a fresh page has the values from design question 2.
func TestHeaderInitEmptyLeaf(t *testing.T) {
	p := newTestPage()
	p[0] = 0x55 // pretend the version word holds something
	for _, f := range fields {
		f.set(p, maxFor(f.size)) // dirty every field first
	}
	p.Init(KindLeaf)
	for _, f := range fields {
		want := uint64(0)
		if f.name == "gen" {
			want = maxFor(f.size)
		}
		if f.name == "heapStart" {
			want = PageSize
		}
		if got := f.get(p); got != want {
			t.Errorf("after Init: %s = %d, want %d", f.name, got, want)
		}
	}
	if p[0] != 0x55 {
		t.Errorf("Init changed the version word")
	}
}

// Test 5: test pages are 8-byte aligned.
func TestTestPageAligned(t *testing.T) {
	for i := 0; i < 1000; i++ {
		if uintptr(unsafe.Pointer(newTestPage()))%8 != 0 {
			t.Fatal("misaligned test page")
		}
	}
}

func BenchmarkSlotCount(b *testing.B) {
	p := newTestPage()
	p.SetSlotCount(300)
	var sink uint16
	for i := 0; i < b.N; i++ {
		sink += p.SlotCount()
	}
	_ = sink
}

func BenchmarkSetSlotCount(b *testing.B) {
	p := newTestPage()
	for i := 0; i < b.N; i++ {
		p.SetSlotCount(uint16(i))
	}
}
