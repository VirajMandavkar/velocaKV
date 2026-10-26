package tree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"velocakv/page"
)

var errNeedsParent = errors.New("tree: split below the root not implemented yet")

// commonPrefix returns how many leading bytes a and b share.
func commonPrefix(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// ---------- page reservation ----------

// reserve allocates pages until n are held. On failure everything reserved
// so far is released and the error returned: nothing in the tree changed.
func (w *Worker) reserve(n int) error {
	for w.nspare < n {
		id, _, err := w.aw.Alloc()
		if err != nil {
			w.release()
			return err
		}
		w.spare[w.nspare] = id
		w.nspare++
	}
	return nil
}

// release frees every reserved page that was not committed.
func (w *Worker) release() {
	for w.nspare > 0 {
		w.nspare--
		w.aw.Free(w.spare[w.nspare])
	}
}

// peekSpare returns the top reserved page and its desc without taking it.
// The caller commits it with w.nspare-- only once the page is in the tree;
// until then, release frees it.
func (w *Worker) peekSpare() (*page.Page, desc) {
	id := w.spare[w.nspare-1]
	p := w.aw.Page(id)
	return p, makeDesc(id, p.Gen())
}

// ---------- split ----------

// splitPage splits the full page p (desc d, leaf or inner) at its byte
// median: p keeps the left half (same ID and gen), R becomes the right
// half. R is a fresh page from the caller, with desc rd. The separator for
// the parent is built in buf (cap page.MaxKeyLen) and returned.
//
//	leaf:  sep = shortest key between the halves; every record stays (copy)
//	inner: sep = the middle separator, in neither half (promote); its child
//	       becomes the left half's upperChild
//
// p is not modified until R is complete. On error p is unchanged and the
// caller still owns R.
func (w *Worker) splitPage(p *page.Page, d desc, R *page.Page, rd desc, buf []byte) ([]byte, error) {
	kind := p.Kind()
	n := int(p.SlotCount())
	m := p.SplitPoint()

	var sep []byte
	var leftUpper uint64
	leftEnd, rightStart := m, m

	switch kind {
	case page.KindLeaf:
		if n < 2 {
			return nil, fmt.Errorf("%w: full leaf %d holds %d records", ErrCorrupt, d.id(), n)
		}
		left := p.KeyAt(m-1, w.keyBuf[:0])
		right := p.KeyAt(m, buf[:0])
		sep = right[:commonPrefix(left, right)+1]

	case page.KindInner:
		if n < 3 {
			return nil, fmt.Errorf("%w: full inner %d holds %d separators", ErrCorrupt, d.id(), n)
		}
		m = min(m, n-2) // the right half needs at least one separator
		leftEnd, rightStart = m, m+1
		sep = p.KeyAt(m, buf[:0])
		c, ok := p.ChildAt(m)
		if !ok {
			return nil, fmt.Errorf("%w: inner %d slot %d is not a child link", ErrCorrupt, d.id(), m)
		}
		leftUpper = c

	default:
		return nil, fmt.Errorf("%w: split of page %d with kind %d", ErrCorrupt, d.id(), kind)
	}

	// Right half [sep, old high): complete before anything links to it.
	high, _ := p.HighFence() // nil = +inf
	if err := R.Format(kind, sep, high); err != nil {
		return nil, err
	}
	if err := p.CopyRange(R, rightStart, n); err != nil {
		return nil, err
	}
	if kind == page.KindInner {
		R.SetUpperChild(p.UpperChild())
	}
	R.SetRightSib(p.RightSib())

	// Left half [old low, sep), built in scratch.
	s := w.env.Scratch
	if err := s.Format(kind, p.LowFence(), sep); err != nil {
		return nil, err
	}
	if err := p.CopyRange(s, 0, leftEnd); err != nil {
		return nil, err
	}
	if kind == page.KindInner {
		s.SetUpperChild(leftUpper) // before Adopt: Adopt copies upperChild from scratch
	}

	// Point of no return: p becomes the left half, keeping its ID and gen.
	p.Adopt(s)
	p.SetRightSib(uint64(rd)) // after Adopt: Adopt copied scratch's rightSib (0)
	return sep, nil
}

// newRoot grows the tree by one level: [sep -> left], upperChild -> right.
func (w *Worker) newRoot(sep []byte, left, right desc) error {
	if desc(w.t.root.Load()) != left {
		return fmt.Errorf("%w: the split ppage %d is not the root", ErrCorrupt, left.id())
	}

	N, nd := w.peekSpare()
	if err := N.Format(page.KindInner, nil, nil); err != nil {
		return err
	}
	if err := w.insertSep(N, sep, left); err != nil {
		return err
	}
	N.SetUpperChild(uint64(right))
	w.nspare--
	w.t.root.Store(uint64(nd))
	return nil
}

// addChild records that child left split at sep: lleft now owns [...,sep),
// right owns [sep,...). It inserts sep -> left first, then points the entry
// that routed to left at right. In between both entries reach left, and
// move right covers key >= sep. ErrNeedSplit means the parent is full and unchanged.
func (w *Worker) addChild(parent *page.Page, sep []byte, left, right desc) error {
	suffix, err := cutKey(parent, sep)
	if err != nil {
		return err
	}

	idx, found, ok := parent.SeekSlot(suffix)
	if !ok || found {
		return fmt.Errorf("%w: separator %q is already in the parent", ErrCorrupt, sep)
	}

	n := int(parent.SlotCount())
	old, ok := parent.UpperChild(), true
	if idx < n {
		old, ok = parent.ChildAt(idx)
	}
	if !ok || desc(old) != left {
		return fmt.Errorf("%w: parent does not route %q to page %d", ErrCorrupt, sep, left.id())
	}

	if err := w.insertSep(parent, sep, left); err != nil {
		return err
	}
	if idx < n {
		if !parent.SetChildAt(idx+1, uint64(right)) {
			return fmt.Errorf("%w: redirect of slot %d failed", ErrCorrupt, idx+1)
		}
	} else {
		parent.SetUpperChild(uint64(right))
	}

	return nil
}

// split makes room for a Put at the full leaf p (desc d), found by the last
// descend. It splits p, then climbs w.path: each parent gets the new
// separator; a full parent splits too and its separator climbs one more
// level; when the root splits, a new root grows on top.
//
// Every page the climb may need (depth + 2) is reserved first, so a failure
// to allocate changes nothing.
func (w *Worker) split(p *page.Page, d desc) error {
	if err := w.reserve(w.depth + 2); err != nil {
		return err
	}
	defer w.release()

	level := w.depth
	R, rd := w.peekSpare()
	sep, err := w.splitPage(p, d, R, rd, w.sep[level][:])
	if err != nil {
		return err
	}
	w.nspare--
	left, right := d, rd

	for {
		if level == 0 {
			return w.newRoot(sep, left, right)
		}
		pd := w.path[level-1]
		parent, err := load(w.aw, pd)
		if err != nil {
			return err
		}

		err = w.addChild(parent, sep, left, right)
		if !errors.Is(err, page.ErrNeedSplit) {
			return err
		}

		PR, prd := w.peekSpare()
		psep, err := w.splitPage(parent, pd, PR, prd, w.sep[level-1][:])
		if err != nil {
			return err
		}
		w.nspare--

		target := parent
		if bytes.Compare(sep, psep) >= 0 {
			target = PR
		}
		if err := w.addChild(target, sep, left, right); err != nil {
			return err
		}

		sep, left, right = psep, pd, prd
		level--
	}

}

// cutkey return key without the page's prefix: the form a page stores and
// searches. Tree code that writes a separator into a page cuts it here, so
// the cut cant be forgotten. It's just a small guard .
func cutKey(p *page.Page, key []byte) ([]byte, error) {
	pl := int(p.PrefixLen())
	if len(key) < pl {
		return nil, fmt.Errorf("%w: key %q shorter than the ppage prefix", ErrCorrupt, key)
	}
	return key[pl:], nil
}

// insertSep stores the separator sep -> child in the inner pagge p.
// The cut by p's prefix happens inside, never at the call site.
func (w *Worker) insertSep(p *page.Page, sep []byte, child desc) error {
	suffix, err := cutKey(p, sep)
	if err != nil {
		return err
	}
	var v [8]byte
	binary.LittleEndian.PutUint64(v[:], uint64(child))
	return p.Insert(suffix, v[:], &w.env)
}
