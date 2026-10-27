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
	ErrArenaFull      = errors.New("arena: full")
	ErrClosed         = errors.New("arena: closed")
	ErrTooManyWorkers = errors.New("arena: too many workers")
	ErrWorkersOpen    = errors.New("arena: workers still open")
)

type Arena struct {
	mu         sync.Mutex // serializes grow, Close and worker registration; guards closed, freeSlots
	slabs      atomic.Pointer[[][]byte]
	maxSlabs   int
	closed     bool
	shards     []shard
	next       atomic.Uint64
	freeSlots  []int // worker slot numbers not in use
	maxWorkers int   // len(freeSlots) when no worker is open; fixed at New
}

// Stats is a snapshot of the arena's page usage. It scans every page
// (through LivePages), so it is for tests and diagnostics only.
type Stats struct {
	Slabs     int // slabs mapped
	MaxSlabs  int // slabs the arena may grow to
	Capacity  int // pages it can ever hand out (ID 0 is reserved)
	HandedOut int // fresh page IDs given out so far, capped at Capacity
	Live      int // allocated and not freed (odd gen)
	Free      int // handed out and freed: on the free lists
}

// split returns the slab number and the byte offset inside that slab for page id.
func split(id uint32) (slab, off int) {
	return int(id >> idShift), int(id&idMask) << page.PageBits
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
	maxWorkers := 4 * runtime.GOMAXPROCS(0)
	a.maxWorkers = maxWorkers
	a.freeSlots = make([]int, a.maxWorkers)
	for i := range a.freeSlots {
		a.freeSlots[i] = len(a.freeSlots) - 1 - i
	}
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

// growLocked is grow's old body, minus the Lock/Unlock lines.
// The caller must hold a.mu.
func (a *Arena) growLocked() (int, error) {
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

// growTo makes sure slab number `slab` is mapped.
func (a *Arena) growTo(slab int) error {
	if slab < len(*a.slabs.Load()) {
		return nil // fast path: already mapped, no lock
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for len(*a.slabs.Load()) <= slab { // re-check under the lock
		if _, err := a.growLocked(); err != nil {
			return err // ErrArenaFull or ErrClosed
		}
	}
	return nil
}

// Close unmaps every slab. The caller must ensure no other goroutine is
// using the arena or any *page.Page from it. Calling Close twice is safe.
func (a *Arena) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.freeSlots) != a.maxWorkers {
		return ErrWorkersOpen
	}
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
	for i := range n {
		idx := (s + i) % n
		id := a.shards[idx].pop(a)
		if id != 0 {
			return id
		}
	}
	return 0
}

// LivePages counts pages that are allocated and not freed (odd gen).
// It looks at every page ever handed out, so it is slow: tests only.
func (a *Arena) LivePages() int {
	n := a.next.Load()
	count := 0
	for id := uint64(1); id <= n; id++ {
		p := a.resolve(uint32(id))
		if p != nil && p.Gen()&1 == 1 {
			count++
		}
	}
	return count
}

func (a *Arena) Stats() Stats {
	// capacity := maxSlabs * PagesPerSlab - 1
	// handed := next, but no more than capacity      (next overshoots after a failed alloc)
	// live := LivePages()
	// return Stats{…, Free: handed - live}
	capacity := a.maxSlabs*int(page.PagesPerSlab) - 1
	handed := min(int(a.next.Load()), capacity)
	live := a.LivePages()

	return Stats{
		Slabs:     len(*a.slabs.Load()),
		MaxSlabs:  a.maxSlabs,
		Capacity:  capacity,
		HandedOut: handed,
		Live:      live,
		Free:      handed - live,
	}
}
