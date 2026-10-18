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
//	 2    16-23    link: next page ID in a chain (0 = end)
//	 3    24-31    rightSib (tree layer)
//	 4    32-39    upperChild (tree layer)
//	 5    40-47    heapStart u16 | deadBytes u16 | lowFenceOff u16 | lowFenceLen u16
//	 6    48-55    highFenceOff u16 | highFenceLen u16 | prefixLen u16 | reserved
//	 7    56-63    reserved
//
// Rules for every method on *Page:
//   - The page must start at an 8-byte-aligned address. Arena pages always
//     do (slab base is 4096-aligned); a Page declared as a Go variable may
//     not, because [N]byte only guarantees 1-byte alignment.
//   - Getters are safe without a lock: each one is a single atomic load.
//     Two getters on the same word may still observe two different versions
//     of the page; readers validate with the version word (locking layer).
//   - Setters require the page write lock (see set).
type Page [PageSize]byte

// Page kinds, stored in header byte 12.
const (
	KindLeaf     = 0 // holds key/value records
	KindInner    = 1 // holds separator keys and child pointers
	KindOverflow = 2 // holds one piece of a big value; no slots, no fences
)

// Page flags, stored in header byte 13.
const FlagAppendHint = 1 << 1 // the last insert went at the end (split hint)

// Byte offset where each header word starts.
const (
	word1 = 8  // gen | kind | flags | slotCount
	word2 = 16 // link
	word3 = 24 // rightSib
	word4 = 32 // upperChild
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

// ---------------------------------------------------------------------------
// Raw word access (unexported building blocks).
// ---------------------------------------------------------------------------

// word returns a pointer to the 8-byte word starting at byte off.
//
//	In:   off  byte offset of the word inside the page
//	Out:  *uint64 aliasing bytes [off, off+8) of the page
//	Rules:
//	  - off must be a multiple of 8, and the page must be 8-byte aligned,
//	    or atomics on the result are misaligned (slow on amd64, crash on arm64).
//	  - off must be at most PageSize-8. Only off >= PageSize is caught (index
//	    panic); off in (PageSize-8, PageSize) would read past the page.
//	  - Callers only pass the header word constants, which satisfy both.
func (p *Page) word(off int) *uint64 { return (*uint64)(unsafe.Pointer(&p[off])) }

// get loads the word at off atomically and extracts one field from it.
//
//	In:   off    word offset (word1, word5, word6)
//	      shift  bit position of the field's lowest bit inside the word
//	      mask   field width mask (mask8, mask16, mask32)
//	Out:  the field value in the low bits; all higher bits are 0
//	Rules: lock-free. The value is from one consistent instant of that word.
func (p *Page) get(off, shift int, mask uint64) uint64 {
	return (atomic.LoadUint64(p.word(off)) >> shift) & mask
}

// set replaces one field in the word at off, leaving the other fields intact.
//
//	In:   off, shift, mask  as for get
//	      v                 new field value; bits above mask are dropped
//	Out:  none
//	Rules:
//	  - The caller must hold the page write lock. Load and store are two
//	    separate steps, so two concurrent setters on the same word would
//	    overwrite each other's change (lost update).
//	  - The store is atomic, so a lock-free reader sees either the whole old
//	    word or the whole new word, never a torn mix.
func (p *Page) set(off, shift int, mask, v uint64) {
	w := p.word(off)
	old := atomic.LoadUint64(w)
	atomic.StoreUint64(w, (old&^(mask<<shift))|((v&mask)<<shift))
}

// ---------------------------------------------------------------------------
// Word 1: gen | kind | flags | slotCount.
// ---------------------------------------------------------------------------

// Gen returns the page's reuse counter.
//
//	Out:   0 for a page never handed out; otherwise how many times the
//	       allocator has handed this page out (wraps at 2^32)
//	Rules: the arena's allocator is the only writer. A (gen, pageID)
//	       reference whose gen differs from Gen() points at a recycled page.
func (p *Page) Gen() uint32 { return uint32(p.get(word1, shiftGen, mask32)) }

// Kind returns the page type: KindLeaf, KindInner or KindOverflow.
func (p *Page) Kind() uint8 { return uint8(p.get(word1, shiftKind, mask8)) }

// Flags returns the flag byte:FlagAppendHint. Test a flag with Flags()&FlagX != 0.
func (p *Page) Flags() uint8 { return uint8(p.get(word1, shiftFlags, mask8)) }

// SlotCount returns the number of records (slots) on the page.
// Slot i lives at byte HeaderSize + 8*i.
func (p *Page) SlotCount() uint16 { return uint16(p.get(word1, shiftSlotCount, mask16)) }

// SetGen overwrites the reuse counter.
//
//	In:    v  new gen
//	Rules: allocator only (arena). Init never changes gen.
func (p *Page) SetGen(v uint32) { p.set(word1, shiftGen, mask32, uint64(v)) }

// SetKind sets the page type. In: KindLeaf, KindInner or KindOverflow.
func (p *Page) SetKind(v uint8) { p.set(word1, shiftKind, mask8, uint64(v)) }

// SetFlags overwrites the whole flag byte. To change one flag, read Flags,
// modify the bit, and write the result back (under the write lock).
func (p *Page) SetFlags(v uint8) { p.set(word1, shiftFlags, mask8, uint64(v)) }

// SetSlotCount sets the number of slots.
//
//	Rules: HeaderSize + 8*v must not exceed HeapStart(); slots and heap
//	       must never overlap (page.Check invariant 1).
func (p *Page) SetSlotCount(v uint16) { p.set(word1, shiftSlotCount, mask16, uint64(v)) }

// ---------------------------------------------------------------------------
// Word 5: heapStart | deadBytes | lowFenceOff | lowFenceLen.
// ---------------------------------------------------------------------------

// HeapStart returns the lowest occupied heap byte.
//
//	Out:  PageSize on an empty page; the heap grows down from there, so a
//	      new record of n bytes is placed at HeapStart()-n
//	Free space = HeapStart() - (HeaderSize + 8*SlotCount()); it is never stored.
func (p *Page) HeapStart() uint16 { return uint16(p.get(word5, shiftHeapStart, mask16)) }

// DeadBytes returns how many heap bytes are freed (by deletes and updates)
// but not yet reclaimed. Compact turns them back into free space.
func (p *Page) DeadBytes() uint16 { return uint16(p.get(word5, shiftDeadBytes, mask16)) }

// LowFenceOff returns the byte offset of the low fence key in the heap.
// Meaningless when LowFenceLen() == 0.
func (p *Page) LowFenceOff() uint16 { return uint16(p.get(word5, shiftLowFenceOff, mask16)) }

// LowFenceLen returns the low fence key's length. 0 means -infinity: the
// page has no lower bound (it is the leftmost page on its level).
func (p *Page) LowFenceLen() uint16 { return uint16(p.get(word5, shiftLowFenceLen, mask16)) }

// SetHeapStart sets the heap boundary.
//
//	Rules: HeaderSize + 8*SlotCount() <= v <= PageSize.
func (p *Page) SetHeapStart(v uint16) { p.set(word5, shiftHeapStart, mask16, uint64(v)) }

// SetDeadBytes sets the dead-byte counter.
func (p *Page) SetDeadBytes(v uint16) { p.set(word5, shiftDeadBytes, mask16, uint64(v)) }

// SetLowFenceOff sets where the low fence key starts in the heap.
func (p *Page) SetLowFenceOff(v uint16) { p.set(word5, shiftLowFenceOff, mask16, uint64(v)) }

// SetLowFenceLen sets the low fence length (0 = -infinity).
//
//	Rules: set Off and Len together under one write lock hold. They are
//	       separate stores, so a lock-free reader can see a new Off with an
//	       old Len and must validate with the version word (P47).
func (p *Page) SetLowFenceLen(v uint16) { p.set(word5, shiftLowFenceLen, mask16, uint64(v)) }

// ---------------------------------------------------------------------------
// Word 6: highFenceOff | highFenceLen | prefixLen | reserved.
// ---------------------------------------------------------------------------

// HighFenceOff returns the byte offset of the high fence key in the heap.
// Meaningless when HighFenceLen() is fenceInf.
func (p *Page) HighFenceOff() uint16 { return uint16(p.get(word6, shiftHighFenceOff, mask16)) }

// HighFenceLen returns the high fence key's length. fenceInf (0xFFFF) means
// +infinity: the page has no upper bound (rightmost page on its level).
// Every key k on the page satisfies lowFence <= k < highFence.
func (p *Page) HighFenceLen() uint16 { return uint16(p.get(word6, shiftHighFenceLen, mask16)) }

// PrefixLen returns how many leading bytes every key on the page shares.
// Those bytes are lowFence[:PrefixLen()] and are not repeated in records;
// a record stores only the key suffix after them.
func (p *Page) PrefixLen() uint16 { return uint16(p.get(word6, shiftPrefixLen, mask16)) }

// SetHighFenceOff sets where the high fence key starts in the heap.
func (p *Page) SetHighFenceOff(v uint16) { p.set(word6, shiftHighFenceOff, mask16, uint64(v)) }

// SetHighFenceLen sets the high fence length (fenceInf = +infinity).
// Same Off/Len pairing rule as SetLowFenceLen.
func (p *Page) SetHighFenceLen(v uint16) { p.set(word6, shiftHighFenceLen, mask16, uint64(v)) }

// SetPrefixLen sets the shared prefix length.
//
//	Rules: v must equal the common prefix length of the two fences;
//	       changing it requires rewriting every record's suffix.
func (p *Page) SetPrefixLen(v uint16) { p.set(word6, shiftPrefixLen, mask16, uint64(v)) }

// ---------------------------------------------------------------------------
// Word 2: link.
// ---------------------------------------------------------------------------

// Link returns the next page ID in whatever chain this page belongs to;
// 0 ends the chain.
//
//	Out:  a page ID in the low 32 bits; the high 32 bits are 0
//	Who uses it:
//	  - live overflow page: next page of the big value
//	  - free page: next page on the arena's free list
//	The two uses never overlap, because a page is either live or free.
//	Rules: lock-free (one atomic load), so the free list may read it on a
//	       page another goroutine just took.
func (p *Page) Link() uint64 { return atomic.LoadUint64(p.word(word2)) }

// SetLink stores the next page ID (0 = end of chain).
//
//	In:    v  a page ID (must fit in 32 bits)
//	Rules: one atomic store. Only the page's current owner writes it:
//	       the overflow writer while the page is live, the free list while
//	       it is free.
func (p *Page) SetLink(v uint64) { atomic.StoreUint64(p.word(word2), v) }

// ---------------------------------------------------------------------------
// Words 3-4: rightSib, upperChild (tree layer).
//
// Each field is a whole 8-byte word holding a page descriptor:
//
//	bits 63..32  gen     the target page's gen when the link was made
//	bits 31..0   pageID  the target page; 0 = no page
//
// A descriptor of 0 means "none". The page package never interprets the
// value; the tree layer packs and checks it (a gen mismatch means the
// target was recycled). Init resets both words to 0.
// ---------------------------------------------------------------------------

// RightSib returns the descriptor of this page's right sibling: the next
// page on the same tree level (B-link). 0 means none (rightmost page).
//
//	Rules: lock-free (one atomic load). A reader that finds a key at or
//	       above this page's high fence follows this link (move right).
func (p *Page) RightSib() uint64 { return atomic.LoadUint64(p.word(word3)) }

// SetRightSib stores the right sibling's descriptor (0 = none).
//
//	In:    v  descriptor: gen<<32 | pageID
//	Rules: the caller holds the page write lock. One atomic store, so a
//	       lock-free reader sees either the old sibling or the new one.
//	       On a split, the new right page must be fully built before this
//	       store publishes it.
func (p *Page) SetRightSib(v uint64) { atomic.StoreUint64(p.word(word3), v) }

// UpperChild returns the descriptor of an inner page's rightmost child:
// the child for keys at or above the last separator. Meaningless on leaf
// and overflow pages.
//
//	Rules: lock-free (one atomic load).
func (p *Page) UpperChild() uint64 { return atomic.LoadUint64(p.word(word4)) }

// SetUpperChild stores an inner page's rightmost-child descriptor.
//
//	In:    v  descriptor: gen<<32 | pageID
//	Rules: the caller holds the page write lock. One atomic store.
func (p *Page) SetUpperChild(v uint64) { atomic.StoreUint64(p.word(word4), v) }

// ---------------------------------------------------------------------------
// Whole-header reset.
// ---------------------------------------------------------------------------

// Init resets the header for a fresh, empty page of the given kind.
//
//	In:   kind  KindLeaf, KindInner or KindOverflow
//	Out:  none. Afterwards: slotCount 0, flags 0, link 0, rightSib 0,
//	      upperChild 0, deadBytes 0, prefixLen 0, heapStart = PageSize,
//	      and both fence lengths 0.
//	Fences: a high fence length of 0 is an EMPTY key, not +infinity
//	        (that is fenceInf). So Init alone is complete only for overflow
//	        pages; leaf and inner pages must go through Format, which calls
//	        Init and then sets real fences.
//	Kept unchanged:
//	  - word 0 (version): belongs to the locking layer
//	  - gen: belongs to the allocator; a reader holding an old
//	    (gen, pageID) reference relies on it to notice the page was reused
//	Rules: the caller must own the page (just allocated, or write-locked).
//	       Word 1 is rebuilt in a local and stored once, so a reader never
//	       sees a half-reset word 1.
//	Body bytes (slots and heap, from byte 64 on) are not cleared.
func (p *Page) Init(kind uint8) {
	old := atomic.LoadUint64(p.word(word1))
	keepGen := old & (mask32 << shiftGen)
	atomic.StoreUint64(p.word(word1), keepGen|uint64(kind)<<shiftKind)
	for off := word1 + 8; off < HeaderSize; off += 8 {
		atomic.StoreUint64(p.word(off), 0)
	}
	p.SetHeapStart(PageSize)
}
