package page

import (
	"errors"
	"fmt"
	"testing"
)

// ---------- helpers ----------

// newLeaf returns a fresh, fenceless leaf page.
func newLeaf(t *testing.T) *Page {
	t.Helper()
	p := newTestPage()
	if err := p.Format(KindLeaf, nil, nil); err != nil {
		t.Fatalf("Format: %v", err)
	}
	return p
}

// pageBDF returns a leaf holding "b", "d", "f", each with value "v".
// Heap layout (8 KiB): b@8190, d@8188, f@8186; heapStart = 8186.
func pageBDF(t *testing.T) *Page {
	t.Helper()
	p := newLeaf(t)
	for _, k := range []string{"b", "d", "f"} {
		if err := p.Insert([]byte(k), []byte("v"), newTestPage()); err != nil {
			t.Fatalf("Insert(%q): %v", k, err)
		}
	}
	return p
}

// fillPage inserts max-size records until one more cannot fit.
// Leaves 1056 B free on 8 KiB (4 records), 312 B on 4 KiB (5 records).
func fillPage(t *testing.T, p *Page) {
	t.Helper()
	count := Usable / (MaxInlineRecord + SlotSize)
	for i := range count {
		key := fmt.Appendf(nil, "k%04d", i)
		val := make([]byte, MaxInlineRecord-len(key))
		if err := p.Insert(key, val, newTestPage()); err != nil {
			t.Fatalf("fill insert %d: %v", i, err)
		}
	}
}

// mustCheck fails the test if any page invariant is broken.
func mustCheck(t *testing.T, p *Page) {
	t.Helper()
	if err := p.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// wantErr fails the test unless err matches want (nil means success).
func wantErr(t *testing.T, op string, err, want error) {
	t.Helper()
	if want == nil && err != nil {
		t.Fatalf("%s: error = %v, want nil", op, err)
	}
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("%s: error = %v, want %v", op, err, want)
	}
}

// ---------- insertAt ----------

func TestInsertAt(t *testing.T) {
	p := newLeaf(t)
	for _, key := range []string{"d", "b", "f", "c"} {
		idx, found, ok := p.SeekSlot([]byte(key))
		if found || !ok {
			t.Fatalf("SeekSlot(%q) = (%d, %t, %t), want not found, ok", key, idx, found, ok)
		}
		p.insertAt(idx, []byte(key), []byte("v"))
		mustCheck(t, p)
	}
	for want, key := range []string{"b", "c", "d", "f"} {
		idx, found, ok := p.SeekSlot([]byte(key))
		if !ok || !found || idx != want {
			t.Errorf("SeekSlot(%q) = (%d, %t, %t), want (%d, true, true)", key, idx, found, ok, want)
		}
	}
}

// ---------- Insert ----------

func TestInsert(t *testing.T) {
	t.Run("duplicate key", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Insert", p.Insert([]byte("d"), []byte("x"), newTestPage()), ErrKeyExists)
		mustCheck(t, p)
	})

	t.Run("not enough space", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p)
		key := []byte("z")
		wantErr(t, "Insert", p.Insert(key, make([]byte, MaxInlineRecord-len(key)), newTestPage()), ErrNeedSplit)
		mustCheck(t, p)
	})

	t.Run("exactly fits", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p)
		key := []byte("z")
		val := make([]byte, p.freeSpace()-SlotSize-len(key)) // 1047 B on 8 KiB, 303 B on 4 KiB
		wantErr(t, "Insert", p.Insert(key, val, newTestPage()), nil)
		if got := p.freeSpace(); got != 0 {
			t.Fatalf("freeSpace = %d, want 0", got)
		}
		mustCheck(t, p)
	})

	t.Run("key too long", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Insert", p.Insert(make([]byte, MaxKeyLen+1), []byte("v"), newTestPage()), ErrTooLarge)
		mustCheck(t, p)
	})

	t.Run("payload over inline limit", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Insert", p.Insert([]byte("c"), make([]byte, MaxInlineRecord), newTestPage()), ErrTooLarge)
		mustCheck(t, p)
	})

	t.Run("empty key on fenceless page", func(t *testing.T) {
		p := newLeaf(t)
		wantErr(t, "Insert", p.Insert(nil, nil, newTestPage()), ErrEmptyKey)
		mustCheck(t, p)
	})

	t.Run("empty value is fine", func(t *testing.T) {
		p := newLeaf(t)
		wantErr(t, "Insert", p.Insert([]byte("a"), nil, newTestPage()), nil)
		mustCheck(t, p)
	})

	t.Run("empty suffix on fenced page is fine", func(t *testing.T) {
		p := newTestPage()
		if err := p.Format(KindLeaf, []byte("user"), []byte("user:z")); err != nil {
			t.Fatalf("Format: %v", err)
		}
		wantErr(t, "Insert", p.Insert(nil, []byte("v"), newTestPage()), nil)
		mustCheck(t, p)
	})
}

// ---------- Update ----------

func TestUpdate(t *testing.T) {
	t.Run("update existing", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Update", p.Update([]byte("d"), []byte("xyz"), newTestPage()), nil)
		if got := p.SlotCount(); got != 3 {
			t.Errorf("SlotCount = %d, want 3", got)
		}
		if got := p.DeadBytes(); got != 2 {
			t.Errorf("DeadBytes = %d, want 2", got)
		}
		if got := string(p.valueAt(1)); got != "xyz" {
			t.Errorf("valueAt(1) = %q, want %q", got, "xyz")
		}
		mustCheck(t, p)
	})

	t.Run("missing key", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Update", p.Update([]byte("c"), []byte("x"), newTestPage()), ErrKeyNotFound)
		if got := p.SlotCount(); got != 3 {
			t.Errorf("SlotCount = %d, want 3", got)
		}
		mustCheck(t, p)
	})

	t.Run("no room (needs compaction, Unit 7)", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p)
		key := []byte("k0000")
		wantErr(t, "Update", p.Update(key, make([]byte, MaxInlineRecord-len(key)), newTestPage()), ErrNeedSplit)
		mustCheck(t, p)
	})
}

// ---------- Delete ----------

func TestDelete(t *testing.T) {
	t.Run("delete middle", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Delete", p.Delete([]byte("d")), nil)
		if got := p.SlotCount(); got != 2 {
			t.Errorf("SlotCount = %d, want 2", got)
		}
		if got := p.DeadBytes(); got != 2 {
			t.Errorf("DeadBytes = %d, want 2", got)
		}
		if idx, found, ok := p.SeekSlot([]byte("f")); idx != 1 || !found || !ok {
			t.Errorf("SeekSlot(f) = (%d, %t, %t), want (1, true, true)", idx, found, ok)
		}
		if got := p.slotAt(2); got != 0 {
			t.Errorf("vacated slot 2 = %#x, want 0 (D12)", uint64(got))
		}
		mustCheck(t, p)
	})

	t.Run("delete last", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Delete", p.Delete([]byte("f")), nil)
		if got := p.SlotCount(); got != 2 {
			t.Errorf("SlotCount = %d, want 2", got)
		}
		if got := p.slotAt(2); got != 0 {
			t.Errorf("vacated slot 2 = %#x, want 0 (D12)", uint64(got))
		}
		mustCheck(t, p)
	})

	t.Run("missing key", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Delete", p.Delete([]byte("c")), ErrKeyNotFound)
		if got := p.SlotCount(); got != 3 {
			t.Errorf("SlotCount = %d, want 3", got)
		}
		mustCheck(t, p)
	})

	t.Run("delete then reinsert", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Delete", p.Delete([]byte("d")), nil)
		wantErr(t, "Insert", p.Insert([]byte("d"), []byte("w"), newTestPage()), nil)
		if got := p.SlotCount(); got != 3 {
			t.Errorf("SlotCount = %d, want 3", got)
		}
		if got := p.DeadBytes(); got != 2 {
			t.Errorf("DeadBytes = %d, want 2", got)
		}
		if got := string(p.valueAt(1)); got != "w" {
			t.Errorf("valueAt(1) = %q, want %q", got, "w")
		}
		mustCheck(t, p)
	})
}
