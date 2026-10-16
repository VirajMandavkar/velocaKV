// Package page implements VelocaKV's on-disk and in-memory page format:
// a fixed-size slotted page that stores sorted, variable-length key/value
// records, plus the overflow chains that hold values too big for one page.
//
// # Page layout
//
// Every page is PageSize bytes (8 KiB by default, 4 KiB with -tags page4k).
// It has three regions. The slot directory grows up from the header; the
// record heap grows down from the end of the page; the gap between them is
// free space.
//
//	0          64                slotOff(n)        heapStart                PageSize
//	| header   | slot 0 | slot 1 |...|   free    | records ... | low | high |
//	|  64 B    |  8 B each, sorted by key        |  newest at  | fence keys |
//	                                             |  the bottom |            |
//
// A slot is one 8-byte word (see slot.go): the record's offset, key length,
// value length, an overflow flag and the first 3 bytes of the key ("head").
// Records themselves are just key suffix bytes followed by value bytes; all
// lengths live in the slot.
//
// # Fences and prefixes
//
// Each page owns a key range [low fence, high fence). Both fences are stored
// at the very top of the heap. Every key on the page shares the fences'
// common prefix, so that prefix is stored once (implicitly, as the start of
// the low fence) and records hold only the remaining "suffix".
//
// # Space accounting
//
// Every byte of the page body is in exactly one bucket, and Check verifies:
//
//	Usable = freeSpace + SlotSize*slotCount + fenceBytes + liveBytes + deadBytes
//
// Deletes and growing updates leave dead bytes behind. Compact packs the live
// records together again, turning dead bytes into free space; Insert and
// Update do this on demand before reporting ErrNeedSplit.
//
// # Overflow records
//
// A record bigger than MaxInlineRecord keeps only its key in the page. Its
// value is split across a chain of overflow pages, and the page stores an
// 8-byte stub instead (first page ID + total length). See overflow.go.
//
// # Concurrency
//
// Header words and slots are read and written with 8-byte atomic operations,
// so a lock-free reader never sees half of a word. Readers validate what
// they read (recordOK, the bounds checks in SeekSlot) and the caller retries
// when validation fails. Writers must hold the page's write lock; locking
// itself lives in a higher layer.
package page
