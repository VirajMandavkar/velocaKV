package page

import "errors"

func (p *Page) freeSpace() int {
	return int(p.HeapStart()) - slotOff(int(p.SlotCount()))
}

func (p *Page) liveBytes() int {
	n := int(p.SlotCount())
	total := 0
	for i := range n {
		s := p.slotAt(i)
		total += s.keyLen() + s.valLen()
	}
	return total
}

// caller has checked that payload fits in free space
func (p *Page) carve(suffix, val []byte) int {
	payload := len(suffix) + len(val)
	off := int(p.HeapStart()) - payload
	copy(p[off:off+len(suffix)], suffix)
	copy(p[off+len(suffix):off+len(suffix)+len(val)], val)
	p.SetHeapStart(uint16(off))
	return off
}

func (p *Page) shiftRight(idx, n int) {
	for i := n - 1; i >= idx; i-- {
		p.setSlotAt(i+1, p.slotAt(i))
	}
}

func (p *Page) insertAt(idx int, suffix, val []byte) {
	n := p.SlotCount()
	off := p.carve(suffix, val)
	p.shiftRight(idx, int(n))
	p.setSlotAt(idx, makeSlot(off, len(suffix), len(val), false, keyHead(suffix)))
	p.SetSlotCount(n + 1)
}

var (
	ErrKeyExists = errors.New("page: key exists")
	ErrNeedSplit = errors.New("page: no room")
	ErrTooLarge  = errors.New("page: record too large")
	ErrEmptyKey  = errors.New("page: empty key")
)

func (p *Page) Insert(suffix, val []byte) error {
	if len(suffix) > MaxKeyLen {
		return ErrTooLarge
	}
	if len(suffix)+len(val) > MaxInlineRecord {
		return ErrTooLarge
	}
	if len(suffix)+len(val) == 0 && p.HeapStart() == PageSize {
		return ErrEmptyKey
	}
	idx, found, ok := p.SeekSlot(suffix)
	if !ok {
		return ErrPageInvariant
	}
	if found {
		return ErrKeyExists
	}

	need := len(suffix) + len(val) + SlotSize
	if need > p.freeSpace() {
		return ErrNeedSplit
	}
	p.insertAt(idx, suffix, val)
	return nil
}
