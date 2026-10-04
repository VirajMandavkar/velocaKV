package page

import "errors"

var (
	ErrKeyExists   = errors.New("page: key exists")
	ErrNeedSplit   = errors.New("page: no room")
	ErrTooLarge    = errors.New("page: record too large")
	ErrEmptyKey    = errors.New("page: empty key")
	ErrKeyNotFound = errors.New("page: key not found")
	ErrNoScratch   = errors.New("scratch must be non-nil if the page may need compaction")
)

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

// scratch must be non-nil if the page may need compaction
func (p *Page) Insert(suffix, val []byte, scratch *Page) error {
	if err := p.checkRecord(suffix, val); err != nil {
		return err
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
		if need > p.freeSpace()+int(p.DeadBytes()) {
			return ErrNeedSplit
		}
		if scratch == nil {
			return ErrNoScratch
		}
		p.Compact(scratch)
	}
	p.insertAt(idx, suffix, val)
	return nil
}

func (p *Page) replaceAt(idx int, suffix, val []byte) {
	old := p.slotAt(idx)
	off := p.carve(suffix, val)
	newSlot := makeSlot(off, len(suffix), len(val), false, keyHead(suffix))
	p.setSlotAt(idx, newSlot)
	deadBytes := int(p.DeadBytes()) + old.keyLen() + old.valLen()
	p.SetDeadBytes(uint16(deadBytes))
}

func (p *Page) checkRecord(suffix, val []byte) error {
	if len(suffix) > MaxKeyLen {
		return ErrTooLarge
	}
	if len(suffix)+len(val) > MaxInlineRecord {
		return ErrTooLarge
	}
	if len(suffix)+len(val) == 0 && p.HeapStart() == PageSize {
		return ErrEmptyKey
	}
	return nil
}

func (p *Page) Update(suffix, val []byte, scratch *Page) error {
	if err := p.checkRecord(suffix, val); err != nil {
		return err
	}
	idx, found, ok := p.SeekSlot(suffix)
	if !ok {
		return ErrPageInvariant
	}
	if !found {
		return ErrKeyNotFound
	}

	need := len(suffix) + len(val)
	if need > p.freeSpace() {
		if need > p.freeSpace()+int(p.DeadBytes()) {
			return ErrNeedSplit
		}
		if scratch == nil {
			return ErrNoScratch
		}
		p.Compact(scratch)
	}
	p.replaceAt(idx, suffix, val)
	return nil
}

func (p *Page) valueAt(idx int) []byte {
	target := p.slotAt(idx)
	off := target.off()
	return p[off+target.keyLen() : off+target.keyLen()+target.valLen()]
}

func (p *Page) shiftLeft(idx, n int) {
	for i := idx; i < n-1; i++ {
		p.setSlotAt(i, p.slotAt(i+1))
	}
	p.setSlotAt(n-1, 0)
}

func (p *Page) Delete(suffix []byte) error {
	idx, found, ok := p.SeekSlot(suffix)
	if !ok {
		return ErrPageInvariant
	}
	if !found {
		return ErrKeyNotFound
	}
	old := p.slotAt(idx)
	n := p.SlotCount()
	p.shiftLeft(idx, int(n))
	p.SetSlotCount(n - 1)
	deadBytes := int(p.DeadBytes()) + old.valLen() + old.keyLen()
	p.SetDeadBytes(uint16(deadBytes))
	return nil
}
