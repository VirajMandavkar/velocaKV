package tree

import (
	"encoding/binary"
	"errors"

	"velocakv/page"
)

// errNeedsParent: splitting a page that has a parent arrives in step 14.
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

// splitLeaf splits the full leaf p (desc d) at its byte median.
//
// Every allocation happens before p is modified, so a failure leaves the
// tree unchanged. Publish order: the right page is complete before
// anything links to it; then p shrinks and links to it; then the parent
// learns the separator. If anything stopped between the last two, keys
// in the right half would still be reachable by moving right from p (P70).
func (w *Worker) splitLeaf(p *page.Page, d desc) error {
	if w.depth > 0 {
		return errNeedsParent // p has a parent: step 14
	}
	n := int(p.SlotCount())
	if n < 2 {
		return ErrCorrupt // a full leaf always holds at least 2 records
	}
	m := p.SplitPoint()

	// Separator: the shortest key with left max < sep <= right min.
	// It lives in w.sepBuf, so it survives p being overwritten below.
	left := p.KeyAt(m-1, w.keyBuf[:0])
	right := p.KeyAt(m, w.sepBuf[:0])
	w.sepBuf = right[:commonPrefix(left, right)+1]
	sep := w.sepBuf

	// Allocate everything before touching p.
	rid, R, err := w.aw.Alloc()
	if err != nil {
		return err
	}
	nid, N, err := w.aw.Alloc()
	if err != nil {
		w.aw.Free(rid)
		return err
	}
	fail := func(err error) error { // until p.Adopt: give both pages back
		w.aw.Free(rid)
		w.aw.Free(nid)
		return err
	}
	rd, nd := makeDesc(rid, R.Gen()), makeDesc(nid, N.Gen())

	// Right half: [sep, old high), built completely before anything links to it.
	high, _ := p.HighFence() // nil means +inf, which Format reads the same way
	if err := R.Format(page.KindLeaf, sep, high); err != nil {
		return fail(err)
	}
	if err := p.CopyRange(R, m, n); err != nil {
		return fail(err)
	}
	R.SetRightSib(p.RightSib())

	// Left half: [old low, sep), built in scratch.
	s := w.env.Scratch
	if err := s.Format(page.KindLeaf, p.LowFence(), sep); err != nil {
		return fail(err)
	}
	if err := p.CopyRange(s, 0, m); err != nil {
		return fail(err)
	}

	// Point of no return: p becomes the left half, keeping its ID and gen.
	p.Adopt(s)
	p.SetRightSib(uint64(rd)) // after Adopt, which copied scratch's rightSib (0)

	// New root: [sep] → p, upperChild → R.
	if err := N.Format(page.KindInner, nil, nil); err != nil {
		return err
	}
	var child [8]byte
	binary.LittleEndian.PutUint64(child[:], uint64(d))
	if err := N.Insert(sep, child[:], &w.env); err != nil {
		return err
	}
	N.SetUpperChild(uint64(rd))
	w.t.root.Store(uint64(nd))
	return nil
}
