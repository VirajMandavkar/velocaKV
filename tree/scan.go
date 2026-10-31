package tree

import (
	"bytes"
	"fmt"
	"iter"
)

// Scan calls fn for every key in [start , end), in key order. A nil start
// means from the first key; a nil end means to the last . fn returns false
// tp stop early.
// k and v are only valid during the call: Scan reuses its buffer for the next
// record, so copy them to keep them . fn must no modify the tree.
func (w *Worker) Scan(start, end []byte, fn func(k, v []byte) bool) error {
	if start != nil && end != nil && bytes.Compare(start, end) >= 0 {
		return nil // empty range
	}

	p, d, err := w.descend(start)
	if err != nil {
		return err
	}
	idx := 0
	if start != nil {
		var ok bool
		idx, _, ok = p.SeekSlot(start[p.PrefixLen():])
		if !ok {
			return fmt.Errorf("%w: seek failed in leaf %d", ErrCorrupt, d.id())
		}
	}
	for {
		// Prefix: copied once per page, not once per record (P69)
		pl := int(p.PrefixLen())
		w.keyBuf = append(w.keyBuf[:0], p.Prefix()...)

		// Where does end fall relative to this page?
		// end < high -> it's inside this page: compare records against it
		// end <= high -> no key to the right
		var endSuf []byte
		lastPage := false
		if end != nil {
			high, inf := p.HighFence()
			c := 1
			if !inf {
				c = bytes.Compare(high, end)
			}
			if c > 0 {
				endSuf = end[pl:]
			}
			lastPage = c >= 0
		}

		n := int(p.SlotCount())
		for i := idx; i < n; i++ {
			suf, ok := p.SuffixAt(i)
			if !ok {
				return fmt.Errorf("%w: leaf %d slot %d unreadable", ErrCorrupt, d.id(), i)
			}
			if endSuf != nil && bytes.Compare(suf, endSuf) >= 0 {
				return nil
			}
			key := append(w.keyBuf[:pl], suf...)
			val, err := p.ValueAt(i, w.valBuf[:0], &w.env)
			if err != nil {
				return fmt.Errorf("leaf %d slot %d: %w", d.id(), i, err)
			}
			w.valBuf = val
			if !fn(key, val) {
				return nil
			}
		}
		if lastPage {
			return nil
		}

		d = desc(p.RightSib())
		if d == 0 {
			return nil
		}
		if p, err = load(w.aw, d); err != nil {
			return err
		}
		idx = 0
	}
}

// Range is Scan as a Go iterator:
//
//	for k, v := range w.Range(start, end) { … }
//	if err := w.Err(); err != nil { … }
//
// The same rules apply: k and v are valid only inside the loop body, and the
// body must not modify the tree. Check Err after the loop.
func (w *Worker) Range(start, end []byte) iter.Seq2[[]byte, []byte] {
	return func(yield func(k, v []byte) bool) {
		w.err = w.Scan(start, end, yield)
	}
}

// Err returns the error that ended the last Range loop, or nil.
func (w *Worker) Err() error { return w.err }
