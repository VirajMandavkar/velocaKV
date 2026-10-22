package tree

import (
	"errors"
	"sync/atomic"
	"velocakv/arena"
	"velocakv/page"
)

var (
	ErrEmptyKey   = errors.New("tree: empty key")
	ErrKeyTooLong = errors.New("tree: key too long")
)

// Tree is a B-link tree whose pages live in an arena.
// It is shared by all goroutines; each goroutines uses its own Worker.
type Tree struct {
	a    *arena.Arena
	root atomic.Uint64
}

// newRoot allocates an empty leaf with no fences and returns its desc.
// Its temporary worker is closed on every return path , so the caller
// can always close the arena afterwards

func newRoot(a *arena.Arena) (desc, error) {
	w, err := a.NewWorker()
	if err != nil {
		return 0, err
	}
	defer w.Close()

	id, p, err := w.Alloc()
	if err != nil {
		return 0, err
	}
	if err := p.Format(page.KindLeaf, nil, nil); err != nil {
		return 0, err
	}
	return makeDesc(id, p.Gen()), nil
}

// New returns a tree whose arena may grow to maxBytes, holding one empty leaf as the root.
func New(maxBytes int64) (*Tree, error) {
	a, err := arena.New(maxBytes)
	if err != nil {
		return nil, err
	}
	root, err := newRoot(a) // its worker is closed by the time this returns
	if err != nil {
		return nil, errors.Join(err, a.Close())
	}
	t := &Tree{a: a}
	t.root.Store(uint64(root))
	return t, nil
}

// Close unmaps all page memory. Every Worker must be closed first;
// otherwise Close returns arena.ErrWorkersOpen and the tree stays usable.
func (t *Tree) Close() error { return t.a.Close() }

// checkKey rejects keys the page format cannot store.
func checkKey(key []byte) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}
	if len(key) > page.MaxKeyLen {
		return ErrKeyTooLong
	}
	return nil
}
