package page

import (
	"bytes"
	"sync/atomic"
)

// slot is one 8-byte slot directory entry describing one record:
//
//	bit 63        51 50       38 37       26   25  24  23       0
//	    | offset 13 | keyLen 13 | valLen 12 | OVF | RSV | head 24 |
//
//	offset  where the record starts in the page
//	keyLen  length of the key suffix
//	valLen  length of the value, or ovfStubSize when OVF is set
//	OVF     the value lives in an overflow chain; the record holds a stub
//	RSV     reserved, always 0
//	head    first 3 bytes of the key suffix, big-endian, zero-padded
//
// Slots are kept sorted by key. Because the head sits in the slot itself, a
// binary search can decide most comparisons without touching the record.
type slot uint64

// Bit positions, derived from the field widths (lowest field first).
const (
	headBits    = 24
	rsvShift    = headBits                     // 24
	ovfShift    = rsvShift + 1                 // 25
	valLenShift = ovfShift + 1                 // 26
	keyLenShift = valLenShift + slotValLenBits // 38
	offsetShift = keyLenShift + slotKeyLenBits // 51

	headMask   = 1<<headBits - 1
	valLenMask = 1<<slotValLenBits - 1
	keyLenMask = 1<<slotKeyLenBits - 1
	offsetMask = 1<<slotOffsetBits - 1
)

// The six fields must fill exactly 64 bits.
const (
	_ = uint(64 - (offsetShift + slotOffsetBits))
	_ = uint((offsetShift + slotOffsetBits) - 64)
)

// makeSlot packs one slot. Each input is masked to its field width, so an
// oversized value can never spill into a neighbouring field. Masking also
// means makeSlot never rejects anything: callers must enforce the real
// limits (MaxKeyLen, MaxInlineRecord) before encoding.
func makeSlot(off, keyLen, valLen int, ovf bool, head uint32) slot {
	s := uint64(off&offsetMask)<<offsetShift |
		uint64(keyLen&keyLenMask)<<keyLenShift |
		uint64(valLen&valLenMask)<<valLenShift |
		uint64(head&headMask)
	if ovf {
		s |= 1 << ovfShift
	}
	return slot(s)
}

func (s slot) off() int     { return int(s>>offsetShift) & offsetMask }
func (s slot) keyLen() int  { return int(s>>keyLenShift) & keyLenMask }
func (s slot) valLen() int  { return int(s>>valLenShift) & valLenMask }
func (s slot) ovf() bool    { return s>>ovfShift&1 == 1 }
func (s slot) head() uint32 { return uint32(s) & headMask }

// The with* helpers return a copy of s with one field replaced.
// A slot is a plain value: they never change the page. Store the result
// with setSlotAt.

// withOff replaces the record offset (Compact moves records).
func (s slot) withOff(off int) slot {
	return s&^slot(offsetMask<<offsetShift) | slot((uint64(off)&offsetMask)<<offsetShift)
}

// withValLen replaces the value length (in-place updates).
func (s slot) withValLen(n int) slot {
	return s&^slot(valLenMask<<valLenShift) | slot((uint64(n)&valLenMask)<<valLenShift)
}

// withOvf sets or clears the overflow flag.
func (s slot) withOvf(on bool) slot {
	s &^= 1 << ovfShift
	if on {
		s |= 1 << ovfShift
	}
	return s
}

// keyHead packs the first 3 bytes of a key suffix, big-endian, zero-padded.
// Comparing two heads as integers orders keys exactly like bytes.Compare,
// except that it may report a tie; only then are the full keys compared.
func keyHead(suffix []byte) uint32 {
	switch len(suffix) {
	case 0:
		return 0
	case 1:
		return uint32(suffix[0]) << 16
	case 2:
		return uint32(suffix[0])<<16 | uint32(suffix[1])<<8
	default:
		return uint32(suffix[0])<<16 | uint32(suffix[1])<<8 | uint32(suffix[2])
	}
}

// slotOff is the byte where slot i starts.
func slotOff(i int) int { return HeaderSize + SlotSize*i }

// slotAt reads slot i with one atomic load. The caller checks i < SlotCount().
func (p *Page) slotAt(i int) slot {
	return slot(atomic.LoadUint64(p.word(slotOff(i))))
}

// setSlotAt writes slot i with one atomic store. The caller holds the write lock.
func (p *Page) setSlotAt(i int, s slot) {
	atomic.StoreUint64(p.word(slotOff(i)), uint64(s))
}

// recordOK reports whether the record s describes can be read safely, given
// the heapStart from the same snapshot of the page. A lock-free reader may
// combine words written at different moments (a torn view); this check turns
// such a view into a retry instead of an out-of-bounds read.
func recordOK(s slot, heapStart int) bool {
	end := s.off() + s.keyLen() + s.valLen()
	badOvf := s.ovf() && s.valLen() != ovfStubSize // a real overflow slot always holds an 8-byte stub
	return s.off() >= heapStart && end <= PageSize && s.keyLen() <= MaxKeyLen && !badOvf
}

// cmpHead compares two key heads: -1, 0 or +1.
func cmpHead(a, b uint32) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// cmpSlot compares a search suffix (with its precomputed head) against the
// key of slot s: negative if the search key sorts first. It reads the record
// only when the heads tie. The caller has already checked recordOK(s).
func (p *Page) cmpSlot(s slot, suffix []byte, head uint32) int {
	if c := cmpHead(head, s.head()); c != 0 {
		return c
	}
	return bytes.Compare(suffix, p[s.off():s.off()+s.keyLen()])
}

// SeekSlot finds suffix with a binary search over the slot directory.
//
//	found = true:  slot idx holds exactly this key.
//	found = false: idx is where the key would be inserted (0..SlotCount).
//	ok = false:    the page view is inconsistent; ignore idx and found and retry.
func (p *Page) SeekSlot(suffix []byte) (idx int, found, ok bool) {
	idx, _, found, ok = p.seek(suffix)
	return idx, found, ok
}

// seek is SeekSlot that also returns the slot at idx, already validated with
// recordOK (zero when idx == SlotCount). Readers must use this s rather than
// loading the slot again: under lock-free reads a second load could return a
// different, unchecked slot (P55).
//
// SlotCount and HeapStart are read once, so the whole search works on one
// snapshot of the page.
func (p *Page) seek(suffix []byte) (idx int, s slot, found, ok bool) {
	head := keyHead(suffix)
	n := int(p.SlotCount())
	heapStart := int(p.HeapStart())
	if heapStart > PageSize {
		return 0, 0, false, false // heapStart past the page end: torn header (P56)
	}
	if slotOff(n) > heapStart {
		return 0, 0, false, false // slot directory runs into the heap: torn count
	}

	lo, hi := 0, n // invariant: keys left of lo are < suffix; keys at or right of hi are >= suffix
	for lo < hi {
		mid := lo + (hi-lo)/2
		ms := p.slotAt(mid)
		if !recordOK(ms, heapStart) {
			return 0, 0, false, false
		}
		if p.cmpSlot(ms, suffix, head) > 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}

	idx = lo
	if idx < n { // slot n does not exist: never load it
		s = p.slotAt(idx) // plain '=': assigns the named result, does not shadow it
		if !recordOK(s, heapStart) {
			return 0, 0, false, false
		}
		found = p.cmpSlot(s, suffix, head) == 0
	}
	return idx, s, found, true
}
