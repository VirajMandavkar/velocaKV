package page

import (
	"bytes"
	"errors"
	"fmt"
	"unsafe"
)

// ErrPageInvariant means a page broke one of its rules (plan §Invariants).
var ErrPageInvariant = errors.New("page: invariant violated")

// fenceInf in highFenceLen means the high fence is +infinity (no upper bound).
// A low fence of length 0 means -infinity (no lower bound).
const fenceInf = 0xFFFF

// Format turns p into an empty page of the given kind whose keys k satisfy
// low <= k < high. An empty low means -infinity; an empty high means +infinity.
// Fences are stored at the top of the heap: high fence last, low fence below it.
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

	// High fence: last bytes of the page.
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

// LowFence returns the low fence bytes (empty = -infinity). No copy.
func (p *Page) LowFence() []byte {
	off, n := int(p.LowFenceOff()), int(p.LowFenceLen())
	return p[off : off+n]
}

// HighFence returns the high fence bytes; inf is true for +infinity. No copy.
func (p *Page) HighFence() (fence []byte, inf bool) {
	off, n := int(p.HighFenceOff()), int(p.HighFenceLen())
	if n == fenceInf {
		return nil, true
	}
	return p[off : off+n], false
}

// Prefix returns the bytes every key on this page starts with. No copy.
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

func commonPrefixLen(a, b []byte) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// Check verifies the page's rules and returns the first one broken.
// It grows with every unit; it is for tests and debug builds, not hot paths.
func (p *Page) Check() error {
	// Invariant 6: alignment first, before any atomic load.
	if uintptr(unsafe.Pointer(p))%8 != 0 {
		return fmt.Errorf("%w: page address not 8-byte aligned", ErrPageInvariant)
	}
	kind := p.Kind()
	if kind > KindOverflow {
		return fmt.Errorf("%w: unknown kind %d", ErrPageInvariant, kind)
	}
	if f := p.Flags(); f&^(FlagHasOverflow|FlagAppendHint) != 0 {
		return fmt.Errorf("%w: unknown flag bits %#x", ErrPageInvariant, f)
	}

	// Invariant 1: slots end before the heap begins; heap ends at the page end.
	slotEnd := slotOff(int(p.SlotCount()))
	heapStart := int(p.HeapStart())
	if slotEnd > heapStart || heapStart > PageSize {
		return fmt.Errorf("%w: slotEnd %d, heapStart %d, PageSize %d",
			ErrPageInvariant, slotEnd, heapStart, PageSize)
	}
	if kind == KindOverflow {
		return nil // overflow pages have no fences (Unit 8)
	}

	// Fences: inside [heapStart, PageSize), not overlapping, not too long.
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

	// Invariant 5: prefixLen = common prefix of the fences (0 if high is +inf,
	// because +inf is stored as zero bytes).
	if got, want := int(p.PrefixLen()), commonPrefixLen(low, high); got != want {
		return fmt.Errorf("%w: prefixLen %d, fences share %d", ErrPageInvariant, got, want)
	}
	return nil
}
