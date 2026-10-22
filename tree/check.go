package tree

import (
	"bytes"
	"errors"
	"fmt"
	"velocakv/page"
)

var (
	errPageCount = errors.New("tree: page count mismatch")
	errDoubleRef = errors.New("tree: page referenced twice")
	errShape     = errors.New("tree: shape invariant violated")
)

type levelPage struct {
	d     desc
	right desc // its rightSib
}

type checker struct {
	w         *Worker
	seen      map[uint32]bool
	leafDepth int           // depth of the first leaf found; -1 before
	levels    [][]levelPage // pages per depth, left to right
}

func (c *checker) add(id uint32, what string) error {
	if c.seen[id] {
		return fmt.Errorf("%w: page %d (%s)", errDoubleRef, id, what)
	}
	c.seen[id] = true
	return nil
}

// walk checks the subtree at d, which its parent routes the keys
// [low, high) to. low == nil means -inf; highInf means high is +inf.
func (c *checker) walk(d desc, low, high []byte, highInf bool, depth int) error {
	if depth >= maxHeight {
		return fmt.Errorf("%w: deeper than %d levels", errShape, maxHeight)
	}
	p, err := load(c.w.aw, d)
	if err != nil {
		return fmt.Errorf("page %d at depth %d: %w", d.id(), depth, err)
	}
	if err := c.add(d.id(), "tree"); err != nil {
		return err
	}
	if err := p.Check(); err != nil {
		return fmt.Errorf("page %d: %w", d.id(), err)
	}

	// The page's fences must be exactly the range its parent routes to it.
	if !bytes.Equal(p.LowFence(), low) {
		return fmt.Errorf("%w: page %d low fence %q, parent routes from %q", errShape, d.id(), p.LowFence(), low)
	}
	h, inf := p.HighFence()
	if inf != highInf || (!inf && !bytes.Equal(h, high)) {
		return fmt.Errorf("%w: page %d high fence %q (inf=%v), parent routes to %q (inf=%v)",
			errShape, d.id(), h, inf, high, highInf)
	}

	for len(c.levels) <= depth {
		c.levels = append(c.levels, nil)
	}
	c.levels[depth] = append(c.levels[depth], levelPage{d, desc(p.RightSib())})

	switch p.Kind() {
	case page.KindLeaf:
		if c.leafDepth == -1 {
			c.leafDepth = depth
		} else if depth != c.leafDepth {
			return fmt.Errorf("%w: leaf %d at depth %d, others at %d", errShape, d.id(), depth, c.leafDepth)
		}
		ids, err := p.AppendOverflowIDs(nil, c.w.aw)
		if err != nil {
			return fmt.Errorf("leaf %d overflow: %w", d.id(), err)
		}
		for _, id := range ids {
			if err := c.add(id, "overflow"); err != nil {
				return err
			}
		}
		return nil

	case page.KindInner:
		n := int(p.SlotCount())
		if n == 0 {
			return fmt.Errorf("%w: inner page %d has no separators", errShape, d.id())
		}
		lo := low
		for i := range n {
			sep := p.KeyAt(i, nil) // full key: prefix + suffix
			child, ok := p.ChildAt(i)
			if !ok {
				return fmt.Errorf("%w: inner page %d slot %d is not a child link", errShape, d.id(), i)
			}
			if err := c.walk(desc(child), lo, sep, false, depth+1); err != nil {
				return err
			}
			lo = sep
		}
		return c.walk(desc(p.UpperChild()), lo, high, highInf, depth+1)

	default:
		return fmt.Errorf("%w: page %d has kind %d in the tree", errShape, d.id(), p.Kind())
	}
}

// checkSiblings verifies that, on every level, each page's rightSib is the
// next page to its right, and the rightmost page's is 0.
func (c *checker) checkSiblings() error {
	for depth, row := range c.levels {
		for j, lp := range row {
			var want desc
			if j+1 < len(row) {
				want = row[j+1].d
			}
			if lp.right != want {
				return fmt.Errorf("%w: depth %d page %d rightSib = page %d, want page %d",
					errShape, depth, lp.d.id(), lp.right.id(), want.id())
			}
		}
	}
	return nil
}

// checkTree verifies the whole tree: every page reachable from the root,
// its fences against the range its parent routes to it, equal leaf depth,
// the rightSib chain on every level, and the page accounting (reachable
// pages + one scratch page per open worker == arena live pages).
// workers is the number of open Workers, w included. Slow: tests only.
func checkTree(w *Worker, workers int) error {
	c := &checker{w: w, seen: make(map[uint32]bool), leafDepth: -1}
	if err := c.walk(desc(w.t.root.Load()), nil, nil, true, 0); err != nil {
		return err
	}
	if err := c.checkSiblings(); err != nil {
		return err
	}
	if err := c.add(w.scratchID, "scratch"); err != nil {
		return err
	}
	live := w.t.a.LivePages()
	if got := len(c.seen) + workers - 1; got != live {
		return fmt.Errorf("%w: tree+overflow=%d scratch=%d live=%d",
			errPageCount, len(c.seen)-1, workers, live)
	}
	return nil
}
