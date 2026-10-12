package arena

import (
	"errors"
	"fmt"
	"runtime"
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

var (
	ErrArenaFull = errors.New("arena: full")
	ErrClosed    = errors.New("arena: closed")
)

// split returns the slab number and the byte offset inside that slab for page id.
func split(id uint32) (slab, off int) {
	return int(id >> idShift), int(id&idMask) << page.PageBits
}

type Arena struct {
	mu       sync.Mutex               // serializes grow and Close; guards closed
	slabs    atomic.Pointer[[][]byte] // immutable table; replaced, never modified
	maxSlabs int
	closed   bool
	shards   []shard // one free list per P; index = worker's shard
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
	a.shards = make([]shard, runtime.GOMAXPROCS(0))
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

	if a.closed {
		return 0, ErrClosed
	}

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

// Close unmaps every slab. The caller must ensure no other goroutine is
// using the arena or any *page.Page from it. Calling Close twice is safe.
func (a *Arena) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true

	old := *a.slabs.Load()
	empty := [][]byte{}

	a.slabs.Store(&empty)
	var errs []error
	for _, s := range old {
		if err := unmapSlab(s); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// pushFree returns page id to shard s.
func (a *Arena) pushFree(s int, id uint32) {
	a.shards[s].push(a, id)
}

// popFree takes a free page ID, from shard s first, then stealing from
// the other shards in order. It returns 0 if every shard is empty.
func (a *Arena) popFree(s int) uint32 {
	n := len(a.shards)
	for i := 0; i < n; i++ {
		idx := (s + i) % n
		id := a.shards[idx].pop(a)
		if id != 0 {
			return id
		}
	}
	return 0
}
