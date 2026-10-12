package arena

import "sync/atomic"

type freeList struct {
	head atomic.Uint64 // pack(popCount, top page ID); pack(0, 0) = empty
}

// pack builds a free-list head word: popCount in the high 32 bits,
// page ID in the low 32 bits.
func pack(count, id uint32) uint64 {
	return uint64(count)<<32 | uint64(id)
}

// unpack splits a head word back into popCount and page ID.
func unpack(w uint64) (count, id uint32) {
	count = uint32(w >> 32)
	id = uint32(w)
	return
}

// push puts page id on top of the list. id must be a mapped, nonzero page
// that is not already on the list.
func (l *freeList) push(a *Arena, id uint32) {
	if id == 0 {
		panic("arena: push of page ID 0")
	}
	p := a.resolve(id)
	for {
		old := l.head.Load()
		count, top := unpack(old)
		p.SetLink(uint64(top))
		if l.head.CompareAndSwap(old, pack(count, id)) {
			return
		}
	}
}

// pop removes and returns the top page ID, or 0 if the list is empty.
func (l *freeList) pop(a *Arena) uint32 {
	for {
		old := l.head.Load()
		count, top := unpack(old)
		if top == 0 {
			return 0
		}
		next := uint32(a.resolve(top).Link())
		if l.head.CompareAndSwap(old, pack(count+1, next)) {
			return top
		}
	}
}
