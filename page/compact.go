package page

// Compact packs every live record tightly against the fences, turning all
// dead bytes into free space. Slot order, keys, values, fences and the rest
// of the header are unchanged; only record offsets, heapStart and deadBytes
// change.
//
//	before:  | slots | free | f | dead | b | dead | d | fences |
//	after:   | slots |     free + dead     | f | b | d | fences |
//
// Records are packed into scratch first and then copied back, because packing
// in place could overwrite a record that has not been moved yet. scratch is a
// caller-owned page (one per worker, reused forever), so compaction never
// allocates. It must not be p itself.
//
// Only two header fields are written back through their setters; the header
// is never copied wholesale, so gen and every other field survive.
func (p *Page) Compact(scratch *Page) {
	n := int(p.SlotCount())
	fenceStart := PageSize - p.fenceBytes()
	top := fenceStart

	// Pass 1: pack each record into scratch, in slot order, below the fences.
	for i := 0; i < n; i++ {
		s := p.slotAt(i)
		size := s.keyLen() + s.valLen()
		top -= size
		copy(scratch[top:], p[s.off():s.off()+size])
		scratch.setSlotAt(i, s.withOff(top))
	}

	// Pass 2: copy the packed bytes and the updated slots back.
	copy(p[top:fenceStart], scratch[top:fenceStart])
	for i := 0; i < n; i++ {
		p.setSlotAt(i, scratch.slotAt(i))
	}
	p.SetHeapStart(uint16(top))
	p.SetDeadBytes(0)
}
