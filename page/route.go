package page

import "encoding/binary"

// childDescSize is the value size of every inner record: one child descriptor.
const childDescSize = 8

// Route returns the child descriptor for suffix on an inner page: the child
// of the first separator > suffix, or upperChild if no separator is.
//
//	separators:     "g"         "p"
//	children:   A  ←───→  B  ←───→  C (upperChild)
//	ranges:  [-∞,"g")  ["g","p")  ["p",+∞)
//
// A suffix equal to a separator routes right, matching the fence rule
// low <= key < high. ok is false if the page looks corrupt or torn.
func (p *Page) Route(suffix []byte) (child uint64, ok bool) {
	idx, s, found, ok := p.seek(suffix)
	if !ok {
		return 0, false
	}
	if found {
		idx++
		s = 0
		if idx < int(p.SlotCount()) {
			if slotOff(idx+1) > PageSize {
				return 0, false
			}
			s = p.slotAt(idx)
			if !recordOK(s, int(p.HeapStart())) {
				return 0, false
			}
		}
	}

	if s == 0 {
		return p.UpperChild(), true
	}

	if s.ovf() || s.valLen() != childDescSize {
		return 0, false
	}
	off := s.off() + s.keyLen()
	return binary.LittleEndian.Uint64(p[off : off+childDescSize]), true
}

// ChildAt returns the child desc stored in inner slot i. ok is false if the
// record does not hold exactly 8 bytes. The caller checks i < SlotCount.
func (p *Page) ChildAt(i int) (child uint64, ok bool) {
	s := p.slotAt(i)
	if s.valLen() != childDescSize || s.ovf() {
		return 0, false
	}
	off := s.off() + s.keyLen()
	return binary.LittleEndian.Uint64(p[off : off+childDescSize]), true
}

// SetChildAt overwrites the child desc of inner slot i in place. The record
// keeps its size, so no slot or heap byte moves. ok is false if slot i does
// not hold exactly an 8-byte desc. The caller checks i < SlotCount.
func (p *Page) SetChildAt(i int, child uint64) (ok bool) {
	s := p.slotAt(i)
	if s.valLen() != childDescSize || s.ovf() {
		return false
	}
	off := s.off() + s.keyLen()
	binary.LittleEndian.PutUint64(p[off:off+childDescSize], child)
	return true
}
