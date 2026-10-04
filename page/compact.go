package page

func (s slot) withOff(off int) slot {
	return s&^slot(offsetMask<<offsetShift) | slot(uint64(off)<<offsetShift)
}

func (p *Page) Compact(scratch *Page) {
	n := int(p.SlotCount())
	fenceStart := PageSize - p.fenceBytes()
	top := fenceStart

	for i := 0; i < n; i++ {
		s := p.slotAt(i)
		size := s.keyLen() + s.valLen()
		top -= size
		copy(scratch[top:], p[s.off():s.off()+size])
		scratch.setSlotAt(i, s.withOff(top))
	}
	copy(p[top:fenceStart], scratch[top:fenceStart])
	for i := 0; i < n; i++ {
		p.setSlotAt(i, scratch.slotAt(i))
	}
	p.SetHeapStart(uint16(top))
	p.SetDeadBytes(0)
}
