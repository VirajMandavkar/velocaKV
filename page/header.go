package page

import (
	"sync/atomic"
	"unsafe"
)

type Page [PageSize]byte

// Page kinds (header byte 12)
const (
	KindLeaf     = 0
	KindInner    = 1
	KindOverflow = 2
)

// Flag switches (bits inside header byte 13)
const (
	FlagHasOverflow = 1 << 0 // page holds at least one overflow record
	FlagAppendHint  = 1 << 1 // llast insert went at the end
)

// Header words: byte offset where each 8-byte word starts.
const (
	word1 = 8  // gen | kind | flags | slotCount
	word2 = 16 // link | crc
	word5 = 40 // heapStart | deadBytes | lowFenceOff | lowFenceLen
	word6 = 48 // highFenceOff | highFenceLen | prefixLen | reserved
)

// Field shifts : (field byte - word byte)*8
const (
	shiftGen       = 0
	shiftKind      = 32
	shiftFlags     = 40
	shiftSlotCount = 48

	shiftHeapStart   = 0
	shiftDeadBytes   = 16
	shiftLowFenceOff = 32
	shiftLowFenceLen = 48

	shiftHighFenceOff = 0
	shiftHighFenceLen = 16
	shiftPrefixLen    = 32
)

// Field masks: one per field size.
const (
	mask8  = 0xFF
	mask16 = 0xFFFF
	mask32 = 0xFFFFFFFF
)

// word returns the 8-byte header word starting at byte off
func (p *Page) word(off int) *uint64 { return (*uint64)(unsafe.Pointer(&p[off])) }

// get loads the whole word atomically, then keeps one field.
func (p *Page) get(off, shift int, mask uint64) uint64 {
	return (atomic.LoadUint64(p.word(off)) >> shift) & mask
}

// set replaces one field and stores the whole word back.
// Caller must hold the page write lock (one writer at a time).
func (p *Page) set(off, shift int, mask, v uint64) {
	w := p.word(off)
	old := atomic.LoadUint64(w)
	atomic.StoreUint64(w, (old&^(mask<<shift))|((v&mask)<<shift))
}

// Word 1: gen | kind | flags | slotCount
func (p *Page) Gen() uint32       { return uint32(p.get(word1, shiftGen, mask32)) }
func (p *Page) Kind() uint8       { return uint8(p.get(word1, shiftKind, mask8)) }
func (p *Page) Flags() uint8      { return uint8(p.get(word1, shiftFlags, mask8)) }
func (p *Page) SlotCount() uint16 { return uint16(p.get(word1, shiftSlotCount, mask16)) }

func (p *Page) SetGen(v uint32)       { p.set(word1, shiftGen, mask32, uint64(v)) }
func (p *Page) SetKind(v uint8)       { p.set(word1, shiftKind, mask8, uint64(v)) }
func (p *Page) SetFlags(v uint8)      { p.set(word1, shiftFlags, mask8, uint64(v)) }
func (p *Page) SetSlotCount(v uint16) { p.set(word1, shiftSlotCount, mask16, uint64(v)) }

// Word 5: heapStart | deadBytes | lowFenceOff | lowFenceLen
func (p *Page) HeapStart() uint16   { return uint16(p.get(word5, shiftHeapStart, mask16)) }
func (p *Page) DeadBytes() uint16   { return uint16(p.get(word5, shiftDeadBytes, mask16)) }
func (p *Page) LowFenceOff() uint16 { return uint16(p.get(word5, shiftLowFenceOff, mask16)) }
func (p *Page) LowFenceLen() uint16 { return uint16(p.get(word5, shiftLowFenceLen, mask16)) }

func (p *Page) SetHeapStart(v uint16)   { p.set(word5, shiftHeapStart, mask16, uint64(v)) }
func (p *Page) SetDeadBytes(v uint16)   { p.set(word5, shiftDeadBytes, mask16, uint64(v)) }
func (p *Page) SetLowFenceOff(v uint16) { p.set(word5, shiftLowFenceOff, mask16, uint64(v)) }
func (p *Page) SetLowFenceLen(v uint16) { p.set(word5, shiftLowFenceLen, mask16, uint64(v)) }

// Word 6: highFenceOff | highFenceLen | prefixLen | reserved
func (p *Page) HighFenceOff() uint16 { return uint16(p.get(word6, shiftHighFenceOff, mask16)) }
func (p *Page) HighFenceLen() uint16 { return uint16(p.get(word6, shiftHighFenceLen, mask16)) }
func (p *Page) PrefixLen() uint16    { return uint16(p.get(word6, shiftPrefixLen, mask16)) }

func (p *Page) SetHighFenceOff(v uint16) { p.set(word6, shiftHighFenceOff, mask16, uint64(v)) }
func (p *Page) SetHighFenceLen(v uint16) { p.set(word6, shiftHighFenceLen, mask16, uint64(v)) }
func (p *Page) SetPrefixLen(v uint16)    { p.set(word6, shiftPrefixLen, mask16, uint64(v)) }

// Init resets the header of a new, empty page.
// Bytes 0-7 (the version word) are left alone: they belong to the lock (Chat 4).
func (p *Page) Init(kind uint8) {
	old := atomic.LoadUint64(p.word(word1))
	keep := old & mask32
	newWord := keep | uint64(kind)<<shiftKind
	atomic.StoreUint64(p.word(word1), newWord)
	for off := word1 + 8; off < HeaderSize; off += 8 {
		atomic.StoreUint64(p.word(off), 0)
	}
	p.SetHeapStart(PageSize)
}

func (p *Page) Link() uint64 {
	return atomic.LoadUint64(p.word(word2))
}

func (p *Page) SetLink(v uint64) {
	atomic.StoreUint64(p.word(word2), v)
}
