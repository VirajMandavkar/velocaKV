package arena

import (
	"fmt"
	"velocakv/page"
)

// page returns the live page for id, or nil if id is 0, not mapped,
// or currently free (even gen).
func (a *Arena) page(id uint32) *page.Page {
	if id == 0 {
		return nil
	}
	p := a.resolve(id)
	if p == nil {
		return nil
	}
	if p.Gen()&1 == 0 {
		return nil
	}
	return p
}

// free marks page id free (gen odd → even) and returns it to shard s.
// It panics if id is not a live page: a double free or a bad ID.
func (a *Arena) free(s int, id uint32) {
	p := a.page(id)
	if p == nil {
		panic(fmt.Sprintf("arena: free of non-live page %d", id))
	}
	p.SetGen(p.Gen() + 1)
	a.pushFree(s, id)
}

// alloc returns a live page for worker shard s: a recycled one if any
// shard has one, otherwise a fresh one, mapping a new slab if needed.
func (a *Arena) alloc(s int) (uint32, *page.Page, error) {
	id := a.popFree(s)
	if id == 0 {
		n := a.next.Add(1)
		if n >= uint64(a.maxSlabs)*page.PagesPerSlab {
			return 0, nil, ErrArenaFull
		}
		id = uint32(n)
		slab, _ := split(id)
		if err := a.growTo(slab); err != nil {
			return 0, nil, err
		}
	}
	p := a.resolve(id)
	p.SetGen(p.Gen() + 1)
	return id, p, nil
}
