package tree

import (
	"bytes"
	"fmt"
	"velocakv/page"
)

// descend walks from the root to the leaf that owns key and returns it with
// its desc. Every inner page passed is recorded in w.path[:w.depth], root
// first, for splits to walk back up.
//
// At each page it first moves right while key >= the high fence: a split
// that linked its new right page but has not yet (or could not) add the
// separator to the parent leaves keys reachable only through rightSib (P70).
func (w *Worker) descend(key []byte) (*page.Page, desc, error) {
	w.depth = 0
	d := desc(w.t.root.Load())
	for range maxHeight {
		p, err := load(w.aw, d)
		if err != nil {
			return nil, 0, err
		}

		// Move right while the key is at or above this page's range.
		for {
			high, inf := p.HighFence()
			if inf || bytes.Compare(key, high) < 0 {
				break
			}

			next := desc(p.RightSib())
			if next == 0 {
				return nil, 0, fmt.Errorf("%w: key >= high fence of page %d but no right sibling", ErrCorrupt, d.id())
			}

			d = next
			if p, err = load(w.aw, d); err != nil {
				return nil, 0, err
			}
		}

		pl := int(p.PrefixLen())
		if len(key) < pl {
			return nil, 0, fmt.Errorf("%w: key shorter than prefix of page %d", ErrCorrupt, d.id())
		}

		switch p.Kind() {
		case page.KindLeaf:
			return p, d, nil
		case page.KindInner:
			w.path[w.depth] = d
			w.depth++
			child, ok := p.Route(key[pl:])
			if !ok {
				return nil, 0, fmt.Errorf("%w: inner page %d failed to route", ErrCorrupt, d.id())
			}
			d = desc(child)
		default:
			return nil, 0, fmt.Errorf("%w: page %d has kind %d on the tree path", ErrCorrupt, d.id(), p.Kind())
		}
	}
	return nil, 0, fmt.Errorf("%w: tree deeper than %d levels", ErrCorrupt, maxHeight)
}
