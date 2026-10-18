package page

import (
	"errors"
	"fmt"
	"testing"
	"unsafe"
)

// Shared test helpers. Every test file in this package uses these.

// ---------- pages ----------

// newTestPage returns a zeroed page that is 8-byte aligned by guarantee:
// it is backed by a []uint64-sized array, whose first word is 64-bit aligned.
func newTestPage() *Page {
	return (*Page)(unsafe.Pointer(new([PageSize / 8]uint64)))
}

// newLeaf returns a fresh, fenceless leaf page.
func newLeaf(t *testing.T) *Page {
	t.Helper()
	p := newTestPage()
	if err := p.Format(KindLeaf, nil, nil); err != nil {
		t.Fatalf("Format: %v", err)
	}
	return p
}

// pageBDF returns a leaf holding "b", "d", "f", each with value "v".
// Heap layout (8 KiB): b@8190, d@8188, f@8186; heapStart = 8186.
func pageBDF(t *testing.T) *Page {
	t.Helper()
	p := newLeaf(t)
	for _, k := range []string{"b", "d", "f"} {
		if err := p.Insert([]byte(k), []byte("v"), testEnv()); err != nil {
			t.Fatalf("Insert(%q): %v", k, err)
		}
	}
	return p
}

// fillPage inserts max-size records until one more cannot fit.
// Leaves 1056 B free on 8 KiB (4 records), 312 B on 4 KiB (5 records).
func fillPage(t *testing.T, p *Page) {
	t.Helper()
	count := Usable / (MaxInlineRecord + SlotSize)
	for i := range count {
		key := fmt.Appendf(nil, "k%04d", i)
		val := make([]byte, MaxInlineRecord-len(key))
		if err := p.Insert(key, val, testEnv()); err != nil {
			t.Fatalf("fill insert %d: %v", i, err)
		}
	}
}

// pageWithKeys builds a leaf holding keys (already sorted) with empty values,
// writing slots directly so SeekSlot can be tested without Insert.
func pageWithKeys(t *testing.T, keys []string) *Page {
	t.Helper()
	p := newLeaf(t)
	top := int(p.HeapStart())
	for i, key := range keys {
		top -= len(key)
		copy(p[top:], key)
		p.setSlotAt(i, makeSlot(top, len(key), 0, false, keyHead([]byte(key))))
	}
	p.SetHeapStart(uint16(top))
	p.SetSlotCount(uint16(len(keys)))
	mustCheck(t, p)
	return p
}

// patterned returns n bytes whose content depends on seed, so two values never
// match by accident and a swapped or shifted page shows up in a comparison.
// 251 is prime, so the pattern never lines up with page boundaries.
func patterned(n int, seed byte) []byte {
	v := make([]byte, n)
	for i := range v {
		v[i] = byte(i%251) + seed
	}
	return v
}

// valueAt returns the value area of slot idx (see valueBytes).
// Internal: the slice changes whenever the page does. Get copies instead.
func (p *Page) valueAt(idx int) []byte { return p.valueBytes(p.slotAt(idx)) }

// ---------- assertions ----------

// mustCheck fails the test if any page invariant is broken.
func mustCheck(t *testing.T, p *Page) {
	t.Helper()
	if err := p.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// wantErr fails the test unless err matches want (want nil means success).
func wantErr(t *testing.T, op string, err, want error) {
	t.Helper()
	if want == nil && err != nil {
		t.Fatalf("%s: error = %v, want nil", op, err)
	}
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("%s: error = %v, want %v", op, err, want)
	}
}

// ---------- environment ----------

// testEnv returns an Env with a fresh scratch page and an in-memory page source.
func testEnv() *Env { return &Env{Scratch: newTestPage(), Pages: newMemPages()} }

// memPages is an in-memory PageSource for tests.
//
//	pages[id] is the page with that ID; index 0 is never used (0 = end of chain).
//	Free sets the entry to nil and IDs are never reused, so a use-after-free
//	shows up as a nil page instead of silently reading a newer page.
type memPages struct {
	pages     []*Page
	failAfter int // Alloc fails once this many pages were ever handed out (-1 = never)
}

var _ PageSource = (*memPages)(nil) // compile-time check: memPages implements PageSource

func newMemPages() *memPages {
	return &memPages{pages: []*Page{nil}, failAfter: -1}
}

func (m *memPages) Alloc() (uint32, *Page, error) {
	if m.failAfter != -1 && len(m.pages)-1 >= m.failAfter {
		return 0, nil, errors.New("memPages: out of pages")
	}
	p := newTestPage()
	m.pages = append(m.pages, p)
	return uint32(len(m.pages) - 1), p, nil
}

func (m *memPages) Page(id uint32) *Page {
	if id == 0 || int(id) >= len(m.pages) {
		return nil
	}
	return m.pages[id]
}

func (m *memPages) Free(id uint32) {
	if id == 0 || int(id) >= len(m.pages) {
		return
	}
	m.pages[id] = nil
}

// live counts pages handed out and not yet freed; tests use it to catch leaks.
func (m *memPages) live() int {
	count := 0
	for _, p := range m.pages {
		if p != nil {
			count++
		}
	}
	return count
}

// ---------- reference model ----------

// lowerBound is the obviously-correct reference for SeekSlot: a lower-bound
// binary search over a plain sorted slice.
func lowerBound(keys []string, k string) (idx int, found bool) {
	lo, hi := 0, len(keys)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if keys[mid] < k {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, lo < len(keys) && keys[lo] == k
}
