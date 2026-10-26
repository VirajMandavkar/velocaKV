package tree

import (
	"velocakv/arena"
	"velocakv/page"
)

// maxHeight bounds the descent. 32 levels of 8 KiB pages is far more than
// the arena can ever hold, so hitting it means a link cycle (corruption).
const maxHeight = 32

// Worker is one goroutine's handle on a Tree. It must not be shared.
type Worker struct {
	t         *Tree
	aw        *arena.Worker
	scratchID uint32
	env       page.Env
	closed    bool
	path      [maxHeight]desc                     // inner pages on the last descent, root first
	depth     int                                 // how many entries of path are valid
	keyBuf    []byte                              // scratch for full keys; cap page.MaxKeyLen
	sep       [maxHeight + 1][page.MaxKeyLen]byte // separator buffer per level
	spare     [maxHeight + 1]uint32               // reserved page IDs
	nspare    int                                 // how many are reserved
}

// NewWorker returns a Worker with its own scratch page.
// It fails with arena.ErrTooManyWorkers once every slot is taken.
func (t *Tree) NewWorker() (*Worker, error) {
	aw, err := t.a.NewWorker()
	if err != nil {
		return nil, err
	}
	id, p, err := aw.Alloc()
	if err != nil {
		aw.Close()
		return nil, err
	}
	return &Worker{
		t:         t,
		aw:        aw,
		scratchID: id,
		env:       page.Env{Scratch: p, Pages: aw},
		closed:    false,
		keyBuf:    make([]byte, 0, page.MaxKeyLen),
	}, nil
}

// Close frees the scratch page and releases the arena slot.
// Calling it again does nothing.
func (w *Worker) Close() {
	if w.closed {
		return
	}
	w.closed = true
	w.release()
	w.aw.Free(w.scratchID)
	w.aw.Close()
}
