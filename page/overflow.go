package page

import (
	"encoding/binary"
	"fmt"
)

// Overflow records.
//
// A record whose key+value exceeds MaxInlineRecord keeps only its key in the
// leaf. Its value is split across a chain of overflow pages, and the leaf
// stores an 8-byte stub in place of the value:
//
//	leaf record:    [ key suffix | headPageID u32 | totalLen u32 ]   slot: OVF=1, valLen=8
//	                                    |
//	                                    v
//	overflow pages: [hdr | Usable B] --link--> [hdr | Usable B] --link--> [hdr | rest] --link--> 0
//
// An overflow page has a normal 64-byte header (kind = KindOverflow, link =
// next page ID) followed by raw value bytes: no slots, no keys. Pages do not
// record how full they are; the reader knows from totalLen.

// PageSource is what overflow code needs from a page allocator.
// The arena implements it; tests use an in-memory fake.
type PageSource interface {
	// Alloc returns a fresh page and its ID. ID 0 is never returned:
	// it means "end of chain".
	Alloc() (id uint32, p *Page, err error)

	// Page turns an ID back into a page. It returns nil for an ID that is
	// not a live page, because a corrupt or torn stub can hold any number.
	Page(id uint32) *Page

	// Free gives a page back to the allocator.
	Free(id uint32)
}

// putStub encodes an overflow stub into b[0:8], little-endian.
func putStub(b []byte, headPageID, totalLen uint32) {
	binary.LittleEndian.PutUint32(b[0:4], headPageID)
	binary.LittleEndian.PutUint32(b[4:8], totalLen)
}

// readStub decodes an overflow stub from b[0:8].
func readStub(b []byte) (headPageID, totalLen uint32) {
	return binary.LittleEndian.Uint32(b[0:4]), binary.LittleEndian.Uint32(b[4:8])
}

// overflowPages is how many overflow pages a value of n bytes needs
// (n / Usable, rounded up).
func overflowPages(n int) int {
	return (n + Usable - 1) / Usable
}

// writeChain stores val in a new chain of overflow pages and returns the ID
// of the first one. Each page's link is set only after the next page exists
// and is filled, so a link always points at a complete page. If an Alloc
// fails part-way, every page already written is freed: nothing leaks.
func writeChain(src PageSource, val []byte) (head uint32, err error) {
	if len(val) == 0 {
		return 0, nil
	}
	var prev *Page
	for off := 0; off < len(val); off += Usable {
		id, p, aerr := src.Alloc()
		if aerr != nil {
			freeChain(src, head, overflowPages(len(val)))
			return 0, aerr
		}
		p.Init(KindOverflow)            // link = 0: the newest page ends the chain
		copy(p[HeaderSize:], val[off:]) // copies at most one page's worth
		if prev == nil {
			head = id
		} else {
			prev.SetLink(uint64(id))
		}
		prev = p
	}
	return head, nil
}

// freeChain returns every page of the chain starting at head to src.
// Each link is read before its page is freed (afterwards the page belongs to
// the allocator again). maxPages caps the walk, so a corrupt link that loops
// back into the chain cannot make it run forever.
func freeChain(src PageSource, head uint32, maxPages int) {
	id := head
	for i := 0; id != 0 && i < maxPages; i++ {
		p := src.Page(id)
		if p == nil {
			return
		}
		next := p.Link()
		src.Free(id)
		id = uint32(next)
	}
}

// readChain appends the totalLen-byte value stored in the chain at head to
// dst and returns the result. If dst has enough capacity, nothing is
// allocated. It returns (dst unchanged, false) when the chain is broken: a
// missing page, a page of the wrong kind, or a chain whose length disagrees
// with totalLen. The decreasing remaining count also caps the walk.
func readChain(src PageSource, head, totalLen uint32, dst []byte) ([]byte, bool) {
	start := len(dst)
	remaining := int(totalLen)
	id := head
	for remaining > 0 {
		p := src.Page(id)
		if p == nil || p.Kind() != KindOverflow {
			return dst[:start], false
		}
		n := min(remaining, Usable)
		dst = append(dst, p[HeaderSize:HeaderSize+n]...)
		remaining -= n
		id = uint32(p.Link())
	}
	if id != 0 { // more pages than totalLen accounts for
		return dst[:start], false
	}
	return dst, true
}

// AppendOverflowIDs appends the ID of every overflow page used by p's
// records to dst, in slot order, and checks each chain: every page must be
// a live KindOverflow page, and the chain must have exactly
// overflowPages(totalLen) pages, ending in link 0.
//
// Call it only after p.Check has passed, so every slot's stub lies inside
// the page. It is for checkers and tests, not hot paths.
func (p *Page) AppendOverflowIDs(dst []uint32, src PageSource) ([]uint32, error) {
	for i := range int(p.SlotCount()) {
		s := p.slotAt(i)
		if !s.ovf() {
			continue
		}
		id, totalLen := readStub(p[s.off()+s.keyLen():])
		want := overflowPages(int(totalLen))
		for n := range want {
			op := src.Page(id)
			if op == nil || op.Kind() != KindOverflow {
				return dst, fmt.Errorf("%w: slot %d: chain page %d (id %d) is not a live overflow page",
					ErrPageInvariant, i, n, id)
			}
			dst = append(dst, id)
			id = uint32(op.Link())
		}
		if id != 0 {
			return dst, fmt.Errorf("%w: slot %d: chain longer than %d pages", ErrPageInvariant, i, want)
		}
	}
	return dst, nil
}
