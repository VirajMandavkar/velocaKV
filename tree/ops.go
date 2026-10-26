package tree

import (
	"errors"
	"fmt"
	"velocakv/page"
)

// ErrNotFound is returned by Get and Delete for a missing key.
var ErrNotFound = page.ErrKeyNotFound // same value, so errors.Is matches both

func (w *Worker) Get(key, dst []byte) ([]byte, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	p, err := w.leafFor(key)
	if err != nil {
		return nil, err
	}
	return p.Get(key[p.PrefixLen():], dst, &w.env)
}
func (w *Worker) Put(key, val []byte) error {
	if err := checkKey(key); err != nil {
		return err
	}
	for range maxHeight {
		p, d, err := w.descend(key)
		if err != nil {
			return err
		}
		_, err = p.Put(key[p.PrefixLen():], val, &w.env)
		if !errors.Is(err, page.ErrNeedSplit) {
			return err
		}
		if err := w.split(p, d); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: Put still needs a split after %d attempts", ErrCorrupt, maxHeight)
}
func (w *Worker) Delete(key []byte) error {
	if err := checkKey(key); err != nil {
		return err
	}
	p, err := w.leafFor(key)
	if err != nil {
		return err
	}
	return p.Delete(key[p.PrefixLen():], &w.env)
}

// leafFor returns the leaf that owns key. Step 10: always the root.
// Step 11 replaces its body with a descent; callers dsson't change.
func (w *Worker) leafFor(key []byte) (*page.Page, error) {
	p, _, err := w.descend(key)
	return p, err
}
