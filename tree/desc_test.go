package tree

import (
	"errors"
	"testing"
	"velocakv/arena"
	"velocakv/page"
)

func TestDescEncoding(t *testing.T) {
	d := makeDesc(42, 3)
	if got := d.id(); got != 42 {
		t.Errorf("desc: id() got %d, want 42", got)
	}
	if got := d.gen(); got != 3 {
		t.Errorf("desc: gen() got %d, want 3", got)
	}
	if got, want := uint64(d), uint64(0x00000003_0000002A); got != want {
		t.Errorf("desc got %d, want %d", got, want)
	}
}

func TestLoadRejectsCorruptDescriptor(t *testing.T) {
	a, err := arena.New(page.SlabBytes)
	if err != nil {
		t.Fatalf("arena.New: %v", err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("arena.Close: %v", err)
		}
	})

	w, err := a.NewWorker()
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	t.Cleanup(w.Close)

	id, p, err := w.Alloc()
	if err != nil {
		t.Fatalf("Alloc: %v", err)
	}
	oldDesc := makeDesc(id, p.Gen())

	got, err := load(w, oldDesc)
	if err != nil {
		t.Fatalf("load(%#x): %v, want nil", uint64(oldDesc), err)
	}
	if got != p {
		t.Fatalf("load(%#x) = %p, want allocated page %p", uint64(oldDesc), got, p)
	}

	if _, err := load(w, 0); !errors.Is(err, ErrCorrupt) {
		t.Errorf("load(0) error = %v, want %v", err, ErrCorrupt)
	}

	w.Free(id)
	if _, err := load(w, oldDesc); !errors.Is(err, ErrCorrupt) {
		t.Errorf("load(%#x) after Free error = %v, want %v", uint64(oldDesc), err, ErrCorrupt)
	}

	reusedID, reused, err := w.Alloc()
	if err != nil {
		t.Fatalf("Alloc after Free: %v", err)
	}
	if reusedID != id {
		t.Fatalf("Alloc after Free returned page ID %d, want reused ID %d", reusedID, id)
	}
	if _, err := load(w, oldDesc); !errors.Is(err, ErrCorrupt) {
		t.Errorf("load(%#x) after page ID %d was reused error = %v, want %v", uint64(oldDesc), id, err, ErrCorrupt)
	}
	if got, err := load(w, makeDesc(reusedID, reused.Gen())); err != nil || got != reused {
		t.Errorf("load with the NEW desc = (%p, %v), want (%p, nil)", got, err, reused)
	}
}
