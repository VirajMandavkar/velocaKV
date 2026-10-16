package page

import "math"

// Page geometry. Every size is derived from PageBits (set by the build tag
// in pagesize_8k.go / pagesize_4k.go), so the two builds never drift apart.
const (
	// SlabBytes is the size of one arena slab: the unit of memory the
	// allocator maps at a time.
	SlabBytes = 64 << 20

	// PageSize is the size of one page in bytes.
	PageSize = 1 << PageBits

	// HeaderSize is the fixed page header: eight 8-byte words.
	HeaderSize = 64

	// SlotSize is the size of one slot directory entry.
	SlotSize = 8

	// Usable is the page body: everything after the header.
	Usable = PageSize - HeaderSize

	// MaxKeyLen is the longest key (or key suffix) a page will store.
	MaxKeyLen = 512

	// MaxFenceBytes is the worst-case space taken by the two fence keys.
	MaxFenceBytes = 2 * MaxKeyLen

	// MaxInlineRecord is the largest key+value stored inside a page.
	// It guarantees that 4 maximum records still fit next to maximum fences,
	// so a split always has something to split. Rounded down to 32 bytes.
	// Bigger records keep only their key here and move the value to an
	// overflow chain.
	MaxInlineRecord = ((Usable-MaxFenceBytes)/4 - 8) &^ 31

	// PagesPerSlab is how many pages one slab holds.
	PagesPerSlab = SlabBytes >> PageBits
)

// Slot field widths (the bit layout is in slot.go) and the overflow stub size.
const (
	slotOffsetBits = 13 // record offset inside the page
	slotKeyLenBits = 13 // key suffix length
	slotValLenBits = 12 // value length (or ovfStubSize for overflow records)
	ovfStubSize    = 8  // overflow stub: headPageID uint32 + totalLen uint32
)

// Compile-time asserts. uint(x) fails to compile when x is negative, so each
// line below is a rule the geometry must obey. geometry_assert_test.go checks
// that they really fire for bad page sizes.
const (
	_ = uint((1<<slotOffsetBits - 1) - (PageSize - 1))              // G1: every offset fits 13 bits
	_ = uint(math.MaxUint16 - PageSize)                             // G1: heapStart (= PageSize when empty) fits uint16
	_ = uint(-(HeaderSize % SlotSize))                              // G2: slots are 8-byte aligned
	_ = uint(Usable - MaxFenceBytes - 4*(MaxInlineRecord+SlotSize)) // G3: 4 max records + fences fit
	_ = uint(-(MaxInlineRecord % 32))                               // G4: rounded to 32
	_ = uint(-(SlabBytes % PageSize))                               // G5: a slab holds whole pages
	_ = uint(MaxInlineRecord - (MaxKeyLen + ovfStubSize))           // G6: an overflow stub record fits inline
	_ = uint((1<<slotValLenBits - 1) - MaxInlineRecord)             // G7: an inline value fits valLen
	_ = uint((1<<slotKeyLenBits - 1) - MaxKeyLen)                   // G7: a key fits keyLen
)
