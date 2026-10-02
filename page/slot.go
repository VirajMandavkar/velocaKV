package page

import (
	"bytes"
	"sync/atomic"
)

// slot is one 8-byte directory entry:
//
//	bit 63        51 50       38 37       26   25  24  23       0
//	    | offset 13 | keyLen 13 | valLen 12 | OVF | RSV | head 24 |
type slot uint64

// Bit positions, derived from the field widths (low field first).
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

// makeSlot packs one slot. Each input is masked to its field width,
// so a bad value can never spill into a neighbouring field.
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

// keyHead packs the first 3 bytes of a key suffix, big-endian, zero-padded.
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

// setSlotAt writes slot i with one atomic store. Caller holds the write lock.
func (p *Page) setSlotAt(i int, s slot) {
	atomic.StoreUint64(p.word(slotOff(i)), uint64(s))
}

func recordOK(s slot, heapStart int) bool {
	end := s.off() + s.keyLen() + s.valLen()
	badOvf := s.ovf() && s.valLen() != ovfStubSize
	return s.off() >= heapStart && end <= PageSize && s.keyLen() <= MaxKeyLen && !badOvf
}

func cmpHead(a, b uint32) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (p *Page) cmpSlot(s slot, suffix []byte, head uint32) int {
	c := cmpHead(head, s.head())
	if c == 0 {
		return bytes.Compare(suffix, p[s.off():s.off()+s.keyLen()])
	}
	return c
}

func (p *Page) SeekSlot(suffix []byte) (idx int, found, ok bool) {
	// if string(suffix) == "" {
	// 	return 0, false, false
	// }
	head := keyHead(suffix)
	lo := 0
	n := int(p.SlotCount())
	heapStart := int(p.HeapStart())
	if slotOff(n) > heapStart {
		return 0, false, false
	}
	hi := n
	for lo < hi {
		mid := lo + (hi-lo)/2
		s := p.slotAt(mid)
		if !recordOK(s, heapStart) {
			return 0, false, false
		}
		c := p.cmpSlot(s, suffix, head)
		if c > 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	idx = lo
	if idx < n {
		s := p.slotAt(idx)
		if !recordOK(s, heapStart) {
			return 0, false, false
		}
		found = p.cmpSlot(s, suffix, head) == 0
	}
	return idx, found, true
}
