package arena

import "velocakv/page"

var _ page.PageSource = (*Worker)(nil) // compile-time check

// Worker is one goroutine's handle on the arena. It is not safe for
// concurrent use: each goroutine creates its own with NewWorker.
type Worker struct {
	a      *Arena
	slot   int // 0 … maxWorkers-1; also the EBR slot in Phase 4
	shard  int // slot % len(a.shards)
	closed bool
}

// NewWorker registers a worker and assigns it a slot and a shard.
// It returns ErrTooManyWorkers if all 4×GOMAXPROCS slots are taken.
func (a *Arena) NewWorker() (*Worker, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return nil, ErrClosed
	}
	if len(a.freeSlots) == 0 {
		return nil, ErrTooManyWorkers
	}
	s := a.freeSlots[len(a.freeSlots)-1]           // read the last one
	a.freeSlots = a.freeSlots[:len(a.freeSlots)-1] // shorten by 1
	return &Worker{a: a, slot: s, shard: s % len(a.shards), closed: false}, nil

}

// Close releases the worker's slot. The worker must not be used afterwards.
func (w *Worker) Close() {
	if w.closed {
		return
	}
	w.closed = true
	w.a.mu.Lock()
	w.a.freeSlots = append(w.a.freeSlots, w.slot)
	w.a.mu.Unlock()
}

// Alloc returns a live page: recycled from this worker's shard (or stolen),
// otherwise fresh.
func (w *Worker) Alloc() (uint32, *page.Page, error) {
	return w.a.alloc(w.shard)
}

// Free returns a live page to this worker's shard. It panics on a double free.
func (w *Worker) Free(id uint32) {
	w.a.free(w.shard, id)
}

// Page returns the live page for id, or nil.
func (w *Worker) Page(id uint32) *page.Page {
	return w.a.page(id)
}
