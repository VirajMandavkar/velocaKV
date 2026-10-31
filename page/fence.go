package page

import (
	"bytes"
	"errors"
	"fmt"
	"unsafe"
)

// ErrPageInvariant means a page broke one of its structural rules, either
// because it is corrupt or because a reader saw a torn view of it.
var ErrPageInvariant = errors.New("page: invariant violated")

// fenceInf stored in highFenceLen means the high fence is +infinity (no upper
// bound). A low fence of length 0 means -infinity (no lower bound).
const fenceInf = 0xFFFF

// Format turns p into an empty page of the given kind that owns the keys k
// with low <= k < high. An empty low means -infinity; an empty high means
// +infinity. The fences are stored at the top of the heap (high fence last,
// low fence just below it) and the page prefix is derived from them.
// A rejected Format leaves the page untouched.
func (p *Page) Format(kind uint8, low, high []byte) error {
	if kind != KindLeaf && kind != KindInner {
		return fmt.Errorf("%w: Format kind %d", ErrPageInvariant, kind)
	}
	if len(low) > MaxKeyLen || len(high) > MaxKeyLen {
		return fmt.Errorf("%w: fence longer than MaxKeyLen", ErrPageInvariant)
	}
	if len(high) > 0 && bytes.Compare(low, high) >= 0 {
		return fmt.Errorf("%w: low fence %q >= high fence %q", ErrPageInvariant, low, high)
	}

	p.Init(kind)
	top := PageSize

	// High fence: the last bytes of the page.
	top -= len(high)
	copy(p[top:], high)
	p.SetHighFenceOff(uint16(top))
	if len(high) == 0 {
		p.SetHighFenceLen(fenceInf)
	} else {
		p.SetHighFenceLen(uint16(len(high)))
	}

	// Low fence: just below the high fence.
	top -= len(low)
	copy(p[top:], low)
	p.SetLowFenceOff(uint16(top))
	p.SetLowFenceLen(uint16(len(low)))

	p.SetHeapStart(uint16(top))
	// +infinity is stored as zero bytes, so it shares nothing: prefixLen 0.
	p.SetPrefixLen(uint16(commonPrefixLen(low, high)))
	return nil
}

// LowFence returns the low fence bytes (empty = -infinity).
// The slice points into the page; it is not a copy.
func (p *Page) LowFence() []byte {
	off, n := int(p.LowFenceOff()), int(p.LowFenceLen())
	return p[off : off+n]
}

// HighFence returns the high fence bytes; inf is true for +infinity.
// The slice points into the page; it is not a copy.
func (p *Page) HighFence() (fence []byte, inf bool) {
	off, n := int(p.HighFenceOff()), int(p.HighFenceLen())
	if n == fenceInf {
		return nil, true
	}
	return p[off : off+n], false
}

// Prefix returns the bytes every key on this page starts with.
// The slice points into the page; it is not a copy.
func (p *Page) Prefix() []byte {
	return p.LowFence()[:p.PrefixLen()]
}

// fenceBytes is how many heap bytes the two fences occupy.
func (p *Page) fenceBytes() int {
	n := int(p.LowFenceLen())
	if h := int(p.HighFenceLen()); h != fenceInf {
		n += h
	}
	return n
}

// commonPrefixLen returns how many leading bytes a and b share.
func commonPrefixLen(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// Check verifies every structural rule of the page and returns the first one
// broken, wrapped in ErrPageInvariant. It is for tests and debug builds;
// it reads the whole page, so it never runs on a hot path.
func (p *Page) Check() error {
	// Alignment first: atomic loads on a misaligned page are undefined.
	if uintptr(unsafe.Pointer(p))%8 != 0 {
		return fmt.Errorf("%w: page address not 8-byte aligned", ErrPageInvariant)
	}

	// Header: known kind and flags only.
	kind := p.Kind()
	if kind > KindOverflow {
		return fmt.Errorf("%w: unknown kind %d", ErrPageInvariant, kind)
	}
	f := p.Flags()
	if f&^hintMask != 0 {
		return fmt.Errorf("%w: unknown flag bits %#x", ErrPageInvariant, f)
	}
	if f&hintMask == 3<<hintShift {
		return fmt.Errorf("%w: invalid insert hint (end and front at once)", ErrPageInvariant)
	}

	// Layout: slots end before the heap starts; the heap ends at the page end.
	n := int(p.SlotCount())
	heapStart := int(p.HeapStart())
	if slotOff(n) > heapStart || heapStart > PageSize {
		return fmt.Errorf("%w: slotEnd %d, heapStart %d, PageSize %d",
			ErrPageInvariant, slotOff(n), heapStart, PageSize)
	}
	if kind == KindOverflow {
		return nil // overflow pages have no slots or fences
	}

	// Fences: inside the heap, not overlapping, low < high.
	lo, ln := int(p.LowFenceOff()), int(p.LowFenceLen())
	ho, hn := int(p.HighFenceOff()), int(p.HighFenceLen())
	highInf := hn == fenceInf
	if highInf {
		hn = 0
	}
	if ln > MaxKeyLen || hn > MaxKeyLen {
		return fmt.Errorf("%w: fence length %d/%d > MaxKeyLen", ErrPageInvariant, ln, hn)
	}
	if lo < heapStart || lo+ln > PageSize || ho < heapStart || ho+hn > PageSize {
		return fmt.Errorf("%w: fence outside heap [%d,%d)", ErrPageInvariant, heapStart, PageSize)
	}
	if ln > 0 && hn > 0 && lo < ho+hn && ho < lo+ln {
		return fmt.Errorf("%w: fences overlap", ErrPageInvariant)
	}
	low, high := p[lo:lo+ln], p[ho:ho+hn]
	if !highInf && bytes.Compare(low, high) >= 0 {
		return fmt.Errorf("%w: low fence %q >= high fence %q", ErrPageInvariant, low, high)
	}

	// Prefix: exactly what the fences share.
	if got, want := int(p.PrefixLen()), commonPrefixLen(low, high); got != want {
		return fmt.Errorf("%w: prefixLen %d, fences share %d", ErrPageInvariant, got, want)
	}

	// Records: each readable, below the fences, keys strictly ascending.
	pl := int(p.PrefixLen())
	lowRest, highRest := low[pl:], high[pl:]
	fenceStart := PageSize - p.fenceBytes()
	var prev []byte
	for i := range n {
		s := p.slotAt(i)
		if !recordOK(s, heapStart) {
			return fmt.Errorf("%w: slot %d out of bounds (off %d, key %d, val %d)",
				ErrPageInvariant, i, s.off(), s.keyLen(), s.valLen())
		}
		if end := s.off() + s.keyLen() + s.valLen(); end > fenceStart {
			return fmt.Errorf("%w: slot %d ends at %d, inside fences at %d",
				ErrPageInvariant, i, end, fenceStart)
		}
		key := p[s.off() : s.off()+s.keyLen()]
		if i > 0 && bytes.Compare(prev, key) >= 0 {
			return fmt.Errorf("%w: slot %d key %q not after %q", ErrPageInvariant, i, key, prev)
		}
		if bytes.Compare(key, lowRest) < 0 {
			return fmt.Errorf("%w: slot %d key %q below low fence %q", ErrPageInvariant, i, key, low)
		}
		if !highInf && bytes.Compare(key, highRest) >= 0 {
			return fmt.Errorf("%w: slot %d key %q not below high fence %q", ErrPageInvariant, i, key, high)
		}
		prev = key
	}

	// Accounting: every body byte is in exactly one bucket.
	sum := p.freeSpace() + n*SlotSize + p.fenceBytes() + p.liveBytes() + int(p.DeadBytes())
	if sum != Usable {
		return fmt.Errorf("%w: accounting sum %d, want Usable %d", ErrPageInvariant, sum, Usable)
	}
	return nil
}
