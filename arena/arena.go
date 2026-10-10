package arena

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"velocakv/page"
)

const (
	slabBits = 26
	_        = uint(1<<slabBits - page.SlabBytes) // fails if SlabBytes > 2^slabBits
	_        = uint(page.SlabBytes - 1<<slabBits) // fails if SlabBytes < 2^slabBits
	idShift  = slabBits - page.PageBits
	idMask   = page.PagesPerSlab - 1
)

var ErrArenaFull = errors.New("arena: full")

// split returns the slab number and the byte offset inside that slab for page id.
func split(id uint32) (slab, off int) {
	return int(id >> idShift), int(id&idMask) << page.PageBits
}

type Arena struct {
	mu       sync.Mutex               // held only by grow
	slabs    atomic.Pointer[[][]byte] // immutable table; replaced, never modified
	maxSlabs int
}

// New returns an empty arena that may grow to at most maxBytes of slabs.
func New(maxBytes int64) (*Arena, error) {
	maxSlab := maxBytes / page.SlabBytes
	if maxSlab <= 0 {
		return nil, fmt.Errorf("arena: maxBytes %d is smaller than one %d-byte slab", maxBytes, page.SlabBytes)
	}
	a := &Arena{maxSlabs: int(maxSlab)}
	empty := [][]byte{}
	a.slabs.Store(&empty)
	return a, nil
}

// resolve returns the page for id, or nil if id's slab is not mapped.
func (a *Arena) resolve(id uint32) *page.Page {
	slab, off := split(id)
	slabs := a.slabs.Load()
	if slabs == nil || slab >= len(*slabs) {
		return nil
	}
	return (*page.Page)((*slabs)[slab][off : off+page.PageSize])
}

// grow maps one more slab and publishes it. It returns the new slab's
// index, or ErrArenaFull if the arena already holds maxSlabs slabs.
func (a *Arena) grow() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	old := *a.slabs.Load()
	if len(old) >= a.maxSlabs {
		return 0, ErrArenaFull
	}
	b, err := mapSlab()
	if err != nil {
		return 0, err
	}

	next := make([][]byte, len(old)+1)
	copy(next, old)
	next[len(old)] = b

	a.slabs.Store(&next)

	return len(old), nil
}
