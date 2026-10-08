package page

import (
	"sync/atomic"
	"unsafe"
)

// Page is one fixed-size page. All multi-byte fields are little-endian.
//
// The 64-byte header is eight 8-byte words. Small fields are packed into
// shared words because Go's smallest atomic is 32 bits: a lock-free reader
// loads a whole word atomically and then extracts the field it needs.
//
//	word  bytes    contents
//	 0    0-7      version (owned by the locking layer, never touched here)
//	 1    8-15     gen u32 | kind u8 | flags u8 | slotCount u16
//	 2    16-23    link: next page ID in an overflow chain (0 = end)
//	 3    24-31    rightSib (tree layer)
//	 4    32-39    upperChild (tree layer)
//	 5    40-47    heapStart u16 | deadBytes u16 | lowFenceOff u16 | lowFenceLen u16
//	 6    48-55    highFenceOff u16 | highFenceLen u16 | prefixLen u16 | reserved
//	 7    56-63    reserved
type Page [PageSize]byte

// Page kinds, stored in header byte 12.
const (
	KindLeaf     = 0 // holds key/value records
	KindInner    = 1 // holds separator keys and child pointers
	KindOverflow = 2 // holds one piece of a big value; no slots, no fences
)

// Page flags, stored in header byte 13.
const (
	FlagHasOverflow = 1 << 0 // the page holds at least one overflow record
	FlagAppendHint  = 1 << 1 // the last insert went at the end (split hint)
)

// Byte offset where each header word starts.
const (
	word1 = 8  // gen | kind | flags | slotCount
	word2 = 16 // link
	word5 = 40 // heapStart | deadBytes | lowFenceOff | lowFenceLen
	word6 = 48 // highFenceOff | highFenceLen | prefixLen | reserved
)

// Bit shift of each field inside its word: (field byte - word byte) * 8.
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

// Field masks, one per field width.
const (
	mask8  = 0xFF
	mask16 = 0xFFFF
	mask32 = 0xFFFFFFFF
)

// word returns a pointer to the 8-byte word starting at byte off.
// off must be a multiple of 8 and the page must be 8-byte aligned.
func (p *Page) word(off int) *uint64 { return (*uint64)(unsafe.Pointer(&p[off])) }

// get loads the whole word atomically, then extracts one field.
func (p *Page) get(off, shift int, mask uint64) uint64 {
	return (atomic.LoadUint64(p.word(off)) >> shift) & mask
}

// set replaces one field and stores the whole word back.
// The load and the store are separate steps, so the caller must hold the
// page write lock: two concurrent writers would lose each other's changes.
func (p *Page) set(off, shift int, mask, v uint64) {
	w := p.word(off)
	old := atomic.LoadUint64(w)
	atomic.StoreUint64(w, (old&^(mask<<shift))|((v&mask)<<shift))
}

// Word 1: gen | kind | flags | slotCount.

// Gen is the page's reuse counter. The allocator bumps it each time the page
// is handed out again, so a stale (gen, pageID) reference can be detected.
func (p *Page) Gen() uint32 { return uint32(p.get(word1, shiftGen, mask32)) }

// Kind is KindLeaf, KindInner or KindOverflow.
func (p *Page) Kind() uint8 { return uint8(p.get(word1, shiftKind, mask8)) }

// Flags holds FlagHasOverflow and FlagAppendHint.
func (p *Page) Flags() uint8 { return uint8(p.get(word1, shiftFlags, mask8)) }

// SlotCount is the number of records on the page.
func (p *Page) SlotCount() uint16 { return uint16(p.get(word1, shiftSlotCount, mask16)) }

func (p *Page) SetGen(v uint32)       { p.set(word1, shiftGen, mask32, uint64(v)) }
func (p *Page) SetKind(v uint8)       { p.set(word1, shiftKind, mask8, uint64(v)) }
func (p *Page) SetFlags(v uint8)      { p.set(word1, shiftFlags, mask8, uint64(v)) }
func (p *Page) SetSlotCount(v uint16) { p.set(word1, shiftSlotCount, mask16, uint64(v)) }

// Word 5: heapStart | deadBytes | lowFenceOff | lowFenceLen.

// HeapStart is the lowest used heap byte. The heap grows down from PageSize,
// so a new record is carved just below it.
func (p *Page) HeapStart() uint16 { return uint16(p.get(word5, shiftHeapStart, mask16)) }

// DeadBytes counts heap bytes freed by deletes and updates but not yet
// reclaimed by Compact.
func (p *Page) DeadBytes() uint16 { return uint16(p.get(word5, shiftDeadBytes, mask16)) }

// LowFenceOff and LowFenceLen locate the low fence key (length 0 = -infinity).
func (p *Page) LowFenceOff() uint16 { return uint16(p.get(word5, shiftLowFenceOff, mask16)) }
func (p *Page) LowFenceLen() uint16 { return uint16(p.get(word5, shiftLowFenceLen, mask16)) }

func (p *Page) SetHeapStart(v uint16)   { p.set(word5, shiftHeapStart, mask16, uint64(v)) }
func (p *Page) SetDeadBytes(v uint16)   { p.set(word5, shiftDeadBytes, mask16, uint64(v)) }
func (p *Page) SetLowFenceOff(v uint16) { p.set(word5, shiftLowFenceOff, mask16, uint64(v)) }
func (p *Page) SetLowFenceLen(v uint16) { p.set(word5, shiftLowFenceLen, mask16, uint64(v)) }

// Word 6: highFenceOff | highFenceLen | prefixLen | reserved.

// HighFenceOff and HighFenceLen locate the high fence key
// (length fenceInf = +infinity).
func (p *Page) HighFenceOff() uint16 { return uint16(p.get(word6, shiftHighFenceOff, mask16)) }
func (p *Page) HighFenceLen() uint16 { return uint16(p.get(word6, shiftHighFenceLen, mask16)) }

// PrefixLen is how many leading bytes every key on the page shares. Those
// bytes are the start of the low fence and are not repeated in records.
func (p *Page) PrefixLen() uint16 { return uint16(p.get(word6, shiftPrefixLen, mask16)) }

func (p *Page) SetHighFenceOff(v uint16) { p.set(word6, shiftHighFenceOff, mask16, uint64(v)) }
func (p *Page) SetHighFenceLen(v uint16) { p.set(word6, shiftHighFenceLen, mask16, uint64(v)) }
func (p *Page) SetPrefixLen(v uint16)    { p.set(word6, shiftPrefixLen, mask16, uint64(v)) }

// Word 2: link.

// Link is the ID of the next page in an overflow chain; 0 ends the chain.
func (p *Page) Link() uint64     { return atomic.LoadUint64(p.word(word2)) }
func (p *Page) SetLink(v uint64) { atomic.StoreUint64(p.word(word2), v) }

// Init resets the header for a fresh, empty page of the given kind.
//
// It keeps gen: the allocator owns it, and a reader holding an old
// (gen, pageID) reference relies on it to notice the page was reused.
// Word 1 is rebuilt in a local and stored once, so a reader never sees a
// half-reset word. Word 0 (the version word) is left alone: it belongs to
// the locking layer.
func (p *Page) Init(kind uint8) {
	old := atomic.LoadUint64(p.word(word1))
	keepGen := old & (mask32 << shiftGen)
	atomic.StoreUint64(p.word(word1), keepGen|uint64(kind)<<shiftKind)
	for off := word1 + 8; off < HeaderSize; off += 8 {
		atomic.StoreUint64(p.word(off), 0)
	}
	p.SetHeapStart(PageSize)
}
