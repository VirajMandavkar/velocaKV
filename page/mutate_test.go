package page

import (
	"errors"
	"fmt"
	"testing"
)

func TestInsertAt(t *testing.T) {
	p := newTestPage()
	if err := p.Format(KindLeaf, nil, nil); err != nil {
		t.Fatalf("Format() error = %v", err)
	}

	val := []byte("v")
	for _, key := range []string{"d", "b", "f", "c"} {
		idx, found, ok := p.SeekSlot([]byte(key))
		if found || !ok {
			t.Fatalf("SeekSlot(%q) = (%d, %t, %t), want not found and ok", key, idx, found, ok)
		}
		p.insertAt(idx, []byte(key), val)
		if err := p.Check(); err != nil {
			t.Fatalf("Check() after inserting %q: %v", key, err)
		}
	}

	for idx, key := range []string{"b", "c", "d", "f"} {
		got, found, ok := p.SeekSlot([]byte(key))
		if !ok || !found || got != idx {
			t.Errorf("SeekSlot(%q) = (%d, %t, %t), want (%d, true, true)", key, got, found, ok, idx)
		}
	}
}

func TestInsertErrors(t *testing.T) {
	t.Run("duplicate key", func(t *testing.T) {
		p := pageWithKeys(t, []string{"b", "d", "f"})
		err := p.Insert([]byte("d"), []byte("x"))
		if !errors.Is(err, ErrKeyExists) {
			t.Fatalf("Insert() error = %v, want %v", err, ErrKeyExists)
		}
		if err := p.Check(); err != nil {
			t.Fatalf("Check() after rejected insert: %v", err)
		}
	})

	t.Run("not enough space", func(t *testing.T) {
		p := newTestPage()
		if err := p.Format(KindLeaf, nil, nil); err != nil {
			t.Fatalf("Format() error = %v", err)
		}
		fillPage(t, p)
		key := []byte("z")
		err := p.Insert(key, make([]byte, MaxInlineRecord-len(key)))
		if !errors.Is(err, ErrNeedSplit) {
			t.Fatalf("Insert() error = %v, want %v", err, ErrNeedSplit)
		}
		if err := p.Check(); err != nil {
			t.Fatalf("Check() after rejected insert : %v", err)
		}
	})

	t.Run("exactly fits", func(t *testing.T) {
		p := pageWithKeys(t, []string{"b"})
		fillPage(t, p)
		err := p.Insert([]byte("c"), make([]byte, p.freeSpace()-SlotSize-1))
		if err != nil {
			t.Fatalf("Insert() error= %v, want nil", err)
		}
		if p.freeSpace() != 0 {
			t.Fatalf("FreeSpace got %d , want 0", p.freeSpace())
		}
		if err := p.Check(); err != nil {
			t.Fatalf("Check() after rejected insert: %v", err)
		}
	})

	t.Run("key too long", func(t *testing.T) {
		p := pageWithKeys(t, []string{"b"})
		err := p.Insert(make([]byte, MaxKeyLen+1), []byte("v"))
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("Insert() error= %v, want %v", err, ErrTooLarge)
		}
		if err := p.Check(); err != nil {
			t.Fatalf("Check() after rejected insert: %v", err)
		}
	})

	t.Run("payload over inline limit", func(t *testing.T) {
		p := pageWithKeys(t, []string{"b"})
		err := p.Insert([]byte("c"), make([]byte, MaxInlineRecord))
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("Insert() error= %v, want %v", err, ErrTooLarge)
		}
		if err := p.Check(); err != nil {
			t.Fatalf("Check() after rejected insert: %v", err)
		}
	})

	t.Run("empty key", func(t *testing.T) {
		p := newTestPage()
		if err := p.Format(KindLeaf, nil, nil); err != nil {
			t.Fatalf("Format() error = %v", err)
		}
		err := p.Insert([]byte(""), []byte(""))
		if !errors.Is(err, ErrEmptyKey) {
			t.Fatalf("Insert() error= %v, want %v", err, ErrEmptyKey)
		}
		if err := p.Check(); err != nil {
			t.Fatalf("Check() after rejected insert: %v", err)
		}
	})
	t.Run("empty value is fine", func(t *testing.T) {
		p := newTestPage()
		if err := p.Format(KindLeaf, nil, nil); err != nil {
			t.Fatalf("Format() error = %v", err)
		}
		err := p.Insert([]byte("a"), []byte(""))
		if err != nil {
			t.Fatalf("Insert() error= %v, want nil", err)
		}
		if err := p.Check(); err != nil {
			t.Fatalf("Check() after rejected insert: %v", err)
		}
	})

	t.Run("empty suffix on a fenced page", func(t *testing.T) {
		p := newTestPage()
		if err := p.Format(KindLeaf, []byte("user"), []byte("user:z")); err != nil {
			t.Fatalf("Format() error = %v", err)
		}
		err := p.Insert([]byte(""), []byte("v"))
		if err != nil {
			t.Fatalf("Insert() error= %v, want nil", err)
		}
		if err := p.Check(); err != nil {
			t.Fatalf("Check() after rejected insert: %v", err)
		}
	})
}

func fillPage(t *testing.T, p *Page) {
	t.Helper()
	count := Usable / (MaxInlineRecord + SlotSize)
	for i := range count {
		key := fmt.Appendf(nil, "k%04d", i)
		val := make([]byte, MaxInlineRecord-len(key))
		if err := p.Insert(key, val); err != nil {
			t.Fatalf("fill insert %d: %v", i, err)
		}
	}
}
