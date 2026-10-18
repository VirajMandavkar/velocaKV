package page

import (
	"bytes"
	"errors"
	"testing"
)

// ---------- insertAt ----------

// TestInsertAt inserts keys out of order and checks that the slots end up
// sorted, with every intermediate page passing Check.
func TestInsertAt(t *testing.T) {
	p := newLeaf(t)
	for _, key := range []string{"d", "b", "f", "c"} {
		idx, found, ok := p.SeekSlot([]byte(key))
		if found || !ok {
			t.Fatalf("SeekSlot(%q) = (%d, %t, %t), want not found, ok", key, idx, found, ok)
		}
		p.insertAt(idx, []byte(key), []byte("v"), false)
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
		wantErr(t, "Insert", p.Insert([]byte("d"), []byte("x"), testEnv()), ErrKeyExists)
		mustCheck(t, p)
	})

	t.Run("not enough space", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p)
		key := []byte("z")
		wantErr(t, "Insert", p.Insert(key, make([]byte, MaxInlineRecord-len(key)), testEnv()), ErrNeedSplit)
		mustCheck(t, p)
	})

	t.Run("exactly fits", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p)
		key := []byte("z")
		val := make([]byte, p.freeSpace()-SlotSize-len(key)) // 1047 B on 8 KiB, 303 B on 4 KiB
		wantErr(t, "Insert", p.Insert(key, val, testEnv()), nil)
		if got := p.freeSpace(); got != 0 {
			t.Fatalf("freeSpace = %d, want 0", got)
		}
		mustCheck(t, p)
	})

	t.Run("key too long", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Insert", p.Insert(make([]byte, MaxKeyLen+1), []byte("v"), testEnv()), ErrTooLarge)
		mustCheck(t, p)
	})

	t.Run("payload over inline limit goes to overflow", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Insert", p.Insert([]byte("c"), make([]byte, MaxInlineRecord), testEnv()), nil)
		idx, _, _ := p.SeekSlot([]byte("c"))
		if !p.slotAt(idx).ovf() {
			t.Errorf("slot OVF = false, want true")
		}
		mustCheck(t, p)
	})

	t.Run("empty key on fenceless page", func(t *testing.T) {
		p := newLeaf(t)
		wantErr(t, "Insert", p.Insert(nil, nil, testEnv()), ErrEmptyKey)
		mustCheck(t, p)
	})

	t.Run("empty value is fine", func(t *testing.T) {
		p := newLeaf(t)
		wantErr(t, "Insert", p.Insert([]byte("a"), nil, testEnv()), nil)
		mustCheck(t, p)
	})

	t.Run("empty suffix on fenced page is fine", func(t *testing.T) {
		p := newTestPage()
		if err := p.Format(KindLeaf, []byte("user"), []byte("user:z")); err != nil {
			t.Fatalf("Format: %v", err)
		}
		wantErr(t, "Insert", p.Insert(nil, []byte("v"), testEnv()), nil)
		mustCheck(t, p)
	})
}

func TestInsertOverflow(t *testing.T) {
	value := make([]byte, 20_000)
	for i := range value {
		value[i] = byte(i % 251)
	}

	t.Run("big value goes to a chain", func(t *testing.T) {
		p := newLeaf(t)
		pages := newMemPages()
		env := &Env{Scratch: newTestPage(), Pages: pages}
		wantErr(t, "Insert", p.Insert([]byte("a"), value, env), nil)

		if got := p.SlotCount(); got != 1 {
			t.Fatalf("SlotCount = %d, want 1", got)
		}
		if !p.slotAt(0).ovf() {
			t.Errorf("slot 0 OVF = false, want true")
		}
		stubBytes := p.valueAt(0)
		if len(stubBytes) != 8 {
			t.Fatalf("overflow slot value length = %d, want 8", len(stubBytes))
		}
		id, totalLen := readStub(stubBytes)

		if int(totalLen) != len(value) {
			t.Errorf("stub totalLen = %d, want %d", totalLen, len(value))
		}
		got, ok := readChain(pages, id, totalLen, nil)
		if !ok {
			t.Fatal("readChain: false")
		}
		if !bytes.Equal(got, value) {
			t.Errorf("readChain returned a different value")
		}
		if got := pages.live(); got != overflowPages(len(value)) {
			t.Errorf("live pages = %d, want %d", got, overflowPages(len(value)))
		}
		mustCheck(t, p)
	})

	t.Run("no page source", func(t *testing.T) {
		p := newLeaf(t)
		before := p.SlotCount()
		wantErr(t, "Insert", p.Insert([]byte("a"), value, &Env{Scratch: newTestPage()}), ErrNoPages)
		if got := p.SlotCount(); got != before {
			t.Errorf("SlotCount = %d, want unchanged at %d", got, before)
		}
		mustCheck(t, p)
	})

	t.Run("allocation fails midway", func(t *testing.T) {
		p := newLeaf(t)
		pages := newMemPages()
		pages.failAfter = 1
		before := p.SlotCount()
		err := p.Insert([]byte("a"), value, &Env{Scratch: newTestPage(), Pages: pages})
		if err == nil {
			t.Fatal("Insert succeeded, want allocation error")
		}
		if got := p.SlotCount(); got != before {
			t.Errorf("SlotCount = %d, want unchanged at %d", got, before)
		}
		if got := pages.live(); got != 0 {
			t.Errorf("live pages = %d, want 0 (no leak)", got)
		}
		mustCheck(t, p)
	})
}

func TestPutMaxValueLen(t *testing.T) {
	t.Run("maximum value", func(t *testing.T) {
		p := newLeaf(t)
		src := newMemPages()
		env := &Env{Scratch: newTestPage(), Pages: src}
		value := patterned(MaxValueLen, 7)

		inserted, err := p.Put([]byte("max"), value, env)
		if err != nil || !inserted {
			t.Fatalf("Put = (%v, %v), want (true, nil)", inserted, err)
		}
		got, err := p.Get([]byte("max"), nil, env)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !bytes.Equal(got, value) {
			t.Errorf("Get returned %d bytes that differ from the value", len(got))
		}
		if got := src.live(); got != overflowPages(MaxValueLen) {
			t.Errorf("src.live() = %d, want %d", got, overflowPages(MaxValueLen))
		}
		mustCheck(t, p)
	})

	t.Run("value over maximum is rejected before allocation", func(t *testing.T) {
		p := newLeaf(t)
		src := newMemPages()
		env := &Env{Scratch: newTestPage(), Pages: src}

		inserted, err := p.Put([]byte("too-large"), make([]byte, MaxValueLen+1), env)
		if inserted || !errors.Is(err, ErrTooLarge) {
			t.Fatalf("Put = (%v, %v), want (false, %v)", inserted, err, ErrTooLarge)
		}
		if got := p.SlotCount(); got != 0 {
			t.Errorf("SlotCount() = %d, want 0", got)
		}
		if got := src.live(); got != 0 {
			t.Errorf("src.live() = %d, want 0", got)
		}
		mustCheck(t, p)
	})
}

// ---------- Update ----------

func TestUpdate(t *testing.T) {
	t.Run("update existing", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Update", p.Update([]byte("d"), []byte("xyz"), testEnv()), nil)
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
		wantErr(t, "Update", p.Update([]byte("c"), []byte("x"), testEnv()), ErrKeyNotFound)
		if got := p.SlotCount(); got != 3 {
			t.Errorf("SlotCount = %d, want 3", got)
		}
		mustCheck(t, p)
	})

	t.Run("same-size update needs no space", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p)
		freeBefore := p.freeSpace()
		key := []byte("k0000")
		wantErr(t, "Update", p.Update(key, make([]byte, MaxInlineRecord-len(key)), testEnv()), nil)
		if p.DeadBytes() != 0 {
			t.Fatalf("DeadBytes = %d, want 0", p.DeadBytes())
		}
		if p.freeSpace() != freeBefore {
			t.Fatalf("freeSpace = %d, want %d", p.freeSpace(), freeBefore)
		}
		mustCheck(t, p)
	})
}

// ---------- Delete ----------

func TestDelete(t *testing.T) {
	t.Run("delete middle", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Delete", p.Delete([]byte("d"), testEnv()), nil)
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
		wantErr(t, "Delete", p.Delete([]byte("f"), testEnv()), nil)
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
		wantErr(t, "Delete", p.Delete([]byte("c"), testEnv()), ErrKeyNotFound)
		if got := p.SlotCount(); got != 3 {
			t.Errorf("SlotCount = %d, want 3", got)
		}
		mustCheck(t, p)
	})

	t.Run("delete then reinsert", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Delete", p.Delete([]byte("d"), testEnv()), nil)
		wantErr(t, "Insert", p.Insert([]byte("d"), []byte("w"), testEnv()), nil)
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

func TestDeleteOverflow(t *testing.T) {
	value := make([]byte, 20_000)

	t.Run("frees the chain", func(t *testing.T) {
		p := newLeaf(t)
		src := newMemPages()
		env := &Env{Scratch: newTestPage(), Pages: src}
		wantErr(t, "Insert", p.Insert([]byte("a"), value, env), nil)

		wantErr(t, "Delete", p.Delete([]byte("a"), env), nil)
		if got := p.SlotCount(); got != 0 {
			t.Errorf("SlotCount = %d, want 0", got)
		}
		if got := src.live(); got != 0 {
			t.Errorf("src.live() = %d, want 0", got)
		}
		mustCheck(t, p)
	})

	t.Run("no page source", func(t *testing.T) {
		p := newLeaf(t)
		src := newMemPages()
		insertEnv := &Env{Scratch: newTestPage(), Pages: src}
		wantErr(t, "Insert", p.Insert([]byte("a"), value, insertEnv), nil)
		liveBefore := src.live()

		wantErr(t, "Delete", p.Delete([]byte("a"), nil), ErrNoPages)
		if got := p.SlotCount(); got != 1 {
			t.Errorf("SlotCount = %d, want 1", got)
		}
		if got := src.live(); got != liveBefore {
			t.Errorf("src.live() = %d, want unchanged at %d", got, liveBefore)
		}
		mustCheck(t, p)
	})
}

func TestGet(t *testing.T) {
	big := make([]byte, 20_000)
	for i := range big {
		big[i] = byte(i % 251)
	}
	src := newMemPages()
	env := &Env{Scratch: newTestPage(), Pages: src}

	p := newLeaf(t)
	wantErr(t, "Insert", p.Insert([]byte("big"), big, env), nil)
	wantErr(t, "Insert", p.Insert([]byte("small"), []byte("hello"), env), nil)

	t.Run("inline value", func(t *testing.T) {
		got, err := p.Get([]byte("small"), nil, env)
		wantErr(t, "Get", err, nil)
		if string(got) != "hello" {
			t.Errorf("Get(small) = %q, want %q", got, "hello")
		}
	})

	t.Run("overflow value", func(t *testing.T) {
		got, err := p.Get([]byte("big"), nil, env)
		wantErr(t, "Get", err, nil)
		if !bytes.Equal(got, big) {
			t.Errorf("Get(big) returned %d bytes that differ from the original", len(got))
		}
	})

	t.Run("missing key", func(t *testing.T) {
		_, err := p.Get([]byte("nope"), nil, env)
		wantErr(t, "Get", err, ErrKeyNotFound)
	})

	t.Run("overflow without page source", func(t *testing.T) {
		_, err := p.Get([]byte("big"), nil, nil)
		wantErr(t, "Get", err, ErrNoPages)
	})

	t.Run("appends to dst", func(t *testing.T) {
		got, err := p.Get([]byte("small"), []byte("pre:"), env)
		wantErr(t, "Get", err, nil)
		if string(got) != "pre:hello" {
			t.Errorf("Get = %q, want %q", got, "pre:hello")
		}
	})

	t.Run("zero allocations with a reused buffer", func(t *testing.T) {
		buf := make([]byte, 0, len(big))
		key := []byte("big")
		allocs := testing.AllocsPerRun(100, func() {
			buf, _ = p.Get(key, buf[:0], env)
		})
		if allocs != 0 {
			t.Errorf("Get allocated %.0f times per call, want 0", allocs)
		}
	})

	mustCheck(t, p)
}
