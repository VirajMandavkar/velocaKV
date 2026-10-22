package tree

import (
	"errors"
	"velocakv/arena"
	"velocakv/page"
)

// ErrCorrupt means a page link (desc) is zero, points at a free or unmapped
// page, or carries a stale gen: the page was recycled since the link was made.
var ErrCorrupt = errors.New("tree: corrupt page link")

// desc is a page link: gen in the high 32 bits, page ID in the low 32 bits.
// 0 means no page. The gen detects a link to a page that was recycled.
type desc uint64

func makeDesc(id, gen uint32) desc {
	return desc(uint64(gen)<<32 | uint64(id))
}

func (d desc) id() uint32 {
	return uint32(d)
}

func (d desc) gen() uint32 {
	return uint32(d >> 32)
}

func load(w *arena.Worker, d desc) (*page.Page, error) {
	if d == 0 {
		return nil, ErrCorrupt
	}
	p := w.Page(d.id())
	if p == nil || p.Gen() != d.gen() {
		return nil, ErrCorrupt
	}
	return p, nil
}
