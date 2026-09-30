package page

import "math"

const (
	SlabBytes       = 64 << 20
	PageSize        = 1 << PageBits
	HeaderSize      = 64
	SlotSize        = 8
	Usable          = PageSize - HeaderSize
	MaxKeyLen       = 512
	MaxFenceBytes   = 2 * MaxKeyLen
	MaxInlineRecord = ((Usable-MaxFenceBytes)/4 - 8) &^ 31
	PagesPerSlab    = SlabBytes >> PageBits
)

// Slot field widths (bit layout comes in Unit 3) and overflow stub size.
const (
	slotOffsetBits = 13
	slotKeyLenBits = 13
	slotValLenBits = 12
	ovfStubSize    = 8 // headPageID u32 + totalLen u32
)

// Compile-time asserts: each uint(x) fails to compile if x<0
const (
	_ = uint((1<<slotOffsetBits - 1) - (PageSize - 1))              // G1: every offset fits 13 bits
	_ = uint(math.MaxUint16 - PageSize)                             // G1: heapStart (= PageSize when empty) fits uint16
	_ = uint(-(HeaderSize % SlotSize))                              // G2: slots are 8-byte aligned
	_ = uint(Usable - MaxFenceBytes - 4*(MaxInlineRecord+SlotSize)) // G3: 4 max records + fences fit
	_ = uint(-(MaxInlineRecord % 32))                               // G4: rounded to 32
	_ = uint(-(SlabBytes % PageSize))                               // G5: slab holds whole pages
	_ = uint(MaxInlineRecord - (MaxKeyLen + ovfStubSize))           // G6: overflow stub record fits inline
	_ = uint((1<<slotValLenBits - 1) - MaxInlineRecord)             // G7: inline value fits valLen
	_ = uint((1<<slotKeyLenBits - 1) - MaxKeyLen)                   // G7: key fits keyLen
)
