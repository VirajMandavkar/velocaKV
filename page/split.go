package page

import (
	"fmt"
	"sync/atomic"
)

// SplitPoint returns the index m at which a split divides p's records:
// the left page keeps slots [0, m) and the right page gets [m, n).
// m is the first index where the bytes of slots [0, m) reach half of the
// total (record bytes + 8 slot bytes each), clamped to [1, n-1] so neither
// half is empty. p must hold at least 2 records.
func (p *Page) SplitPoint() int {
	n := int(p.SlotCount())
	total := 0

	for i := range n {
		s := (p.slotAt(i))
		total += s.keyLen() + s.valLen() + SlotSize
	}

	acc, m := 0, 0
	for i := range n {
		s := p.slotAt(i)
		acc += s.keyLen() + s.valLen() + SlotSize
		if 2*acc >= total {
			m = i + 1
			break
		}
	}
	if m >= n {
		m = n - 1
	}
	return m
}

// KeyAt appends the full key of slot i (page prefix + stored suffix) to dst.
func (p *Page) KeyAt(i int, dst []byte) []byte {
	s := p.slotAt(i)
	dst = append(dst, p.Prefix()...)
	dst = append(dst, p[s.off():s.off()+s.keyLen()]...)
	return dst
}

// CopyRange appends records [from, to) of p to the end of dst, in slot order.
// dst must be a formatted page of the same kind whose fences contain those
// keys, and its existing keys (if any) must sort before them.
//
// Each suffix is re-cut for dst's prefix, which is never shorter than p's.
// Values are copied exactly as stored: an overflow record keeps its 8-byte
// stub and OVF flag, so the chain moves without being copied or freed.
// It never compacts and never allocates pages. ErrNeedSplit if dst runs
// out of room; dst is unchanged by the record that did not fit.
func (p *Page) CopyRange(dst *Page, from, to int) error {
	if dst.Kind() != p.Kind() {
		return fmt.Errorf("%w: CopyRange kind %d into kind %d", ErrPageInvariant, p.Kind(), dst.Kind())
	}
	cut := int(dst.PrefixLen()) - int(p.PrefixLen())
	if cut < 0 {
		return fmt.Errorf("%w: CopyRange into a shorter prefix", ErrPageInvariant)
	}

	n, top := int(dst.SlotCount()), int(dst.HeapStart())
	publish := func() { // dst is private until linked: two plain header updates suffice
		dst.SetSlotCount(uint16(n))
		dst.SetHeapStart(uint16(top))
	}
	for i := from; i < to; i++ {
		s := p.slotAt(i)
		off, kl, vl := s.off(), s.keyLen(), s.valLen()
		if cut > kl {
			publish()
			return fmt.Errorf("%w: CopyRange slot %d shorter than the prefix cut", ErrPageInvariant, i)
		}
		suffix := p[off+cut : off+kl]
		val := p[off+kl : off+kl+vl] // an overflow record's value IS its 8-byte stub

		size := len(suffix) + len(val)
		if top-size < slotOff(n+1) {
			publish()
			return ErrNeedSplit
		}
		top -= size
		copy(dst[top:], suffix)
		copy(dst[top+len(suffix):], val)
		dst.setSlotAt(n, makeSlot(top, len(suffix), len(val), s.ovf(), keyHead(suffix)))
		n++
	}
	publish()
	return nil
}

// Adopt makes p a copy of src, except for what p owns: word 0 (the version
// word, owned by the locking layer) and its gen (owned by the arena; every
// link to p carries it). The free gap is not copied: no slot points into it.
// Split uses this: the left half is built in scratch, then adopted by the
// original page, whose ID and gen its parent and left neighbour already hold.
func (p *Page) Adopt(src *Page) {
	gen := p.Gen()
	w1 := atomic.LoadUint64(src.word(word1))

	n, hs := int(src.SlotCount()), int(src.HeapStart())
	copy(p[16:slotOff(n)], src[16:slotOff(n)])
	copy(p[hs:PageSize], src[hs:PageSize])

	w1 &^= mask32 << shiftGen     // clear scratch's gen
	w1 |= uint64(gen) << shiftGen // put P's gen back
	atomic.StoreUint64(p.word(word1), w1)
}

// SplitPointFor returns where to split 'p' before inserting a new key that
// would land at the slot newIdx. After ordered inserts (the hint says the last
// new key want to this same end) it splits next to that end , leaving the
// other half neaerly full; otherwise it is the byte median . p holds at least
// 2 records
func (p *Page) SplitPointFor(newIdx int) int {
	n := int(p.SlotCount())
	switch h := p.hint(); {
	case h == hintEnd && newIdx == n:
		return n - 1
	case h == hintFront && newIdx == 0:
		return 1
	}
	return p.SplitPoint()
}
