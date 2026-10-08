package page

import (
	"bytes"
	"fmt"
	"testing"
)

// pageGrowA returns a full page with dead bytes where "a" holds a 10-byte value.
// Growing "a" to a max-size value cannot go in place and forces compaction.
// 8 KiB: free 1045, dead 1760 · 4 KiB: free 301, dead 736.
func pageGrowA(t *testing.T) *Page {
	t.Helper()
	p := newLeaf(t)
	wantErr(t, "Insert", p.Insert([]byte("a"), make([]byte, 10), nil), nil)
	fillPage(t, p)
	wantErr(t, "Delete", p.Delete([]byte("k0001"), testEnv()), nil)
	return p
}

// pageNeedsCompact returns a full page with one deleted max record:
// a new max-size insert fits only after compaction.
func pageNeedsCompact(t *testing.T) *Page {
	t.Helper()
	p := newLeaf(t)
	fillPage(t, p)
	wantErr(t, "Delete", p.Delete([]byte("k0001"), testEnv()), nil)
	return p
}

// maxVal returns a value that makes key+value exactly MaxInlineRecord.
func maxVal(key []byte) []byte {
	return bytes.Repeat([]byte("u"), MaxInlineRecord-len(key))
}

// wantUnchanged fails if free space or dead bytes moved.
func wantUnchanged(t *testing.T, p *Page, free int, dead uint16) {
	t.Helper()
	if p.freeSpace() != free || p.DeadBytes() != dead {
		t.Errorf("page changed: free %d→%d, dead %d→%d", free, p.freeSpace(), dead, p.DeadBytes())
	}
}

// ---------- Compact itself ----------

func TestCompact(t *testing.T) {
	t.Run("reclaims dead bytes", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Delete", p.Delete([]byte("d"), testEnv()), nil)
		freeBefore := p.freeSpace()

		p.Compact(newTestPage())
		mustCheck(t, p) // structure first, then read

		if got := p.DeadBytes(); got != 0 {
			t.Errorf("DeadBytes = %d, want 0", got)
		}
		if got := int(p.HeapStart()); got != PageSize-4 {
			t.Errorf("HeapStart = %d, want %d", got, PageSize-4)
		}
		if got := p.freeSpace(); got != freeBefore+2 {
			t.Errorf("freeSpace = %d, want %d", got, freeBefore+2)
		}
		for _, k := range []string{"b", "f"} {
			idx, found, ok := p.SeekSlot([]byte(k))
			if !ok || !found {
				t.Fatalf("SeekSlot(%q) = (%d, %t, %t), want found", k, idx, found, ok)
			}
			if got := string(p.valueAt(idx)); got != "v" {
				t.Errorf("value of %q = %q, want %q", k, got, "v")
			}
		}
	})

	t.Run("no-op on a clean page", func(t *testing.T) {
		p := pageBDF(t)
		free, heap := p.freeSpace(), p.HeapStart()

		p.Compact(newTestPage())
		mustCheck(t, p)

		if p.freeSpace() != free || p.HeapStart() != heap || p.DeadBytes() != 0 {
			t.Errorf("clean page changed: free %d→%d, heapStart %d→%d, dead %d",
				free, p.freeSpace(), heap, p.HeapStart(), p.DeadBytes())
		}
	})

	t.Run("keeps gen", func(t *testing.T) {
		p := pageBDF(t)
		p.SetGen(7)
		wantErr(t, "Delete", p.Delete([]byte("d"), testEnv()), nil)

		p.Compact(newTestPage())
		mustCheck(t, p)

		if got := p.Gen(); got != 7 {
			t.Errorf("Gen = %d, want 7", got)
		}
	})

	t.Run("content preserved", func(t *testing.T) {
		p := newLeaf(t)
		want := make(map[string]string) // reference model

		for i := range 20 {
			key := fmt.Appendf(nil, "key_%02d", i)
			val := fmt.Appendf(nil, "val_%02d", i)
			wantErr(t, "Insert", p.Insert(key, val, nil), nil)
			want[string(key)] = string(val)
		}
		for i := 1; i < 20; i += 2 {
			key := fmt.Appendf(nil, "key_%02d", i)
			wantErr(t, "Delete", p.Delete(key, testEnv()), nil)
			delete(want, string(key))
		}

		p.Compact(newTestPage())
		mustCheck(t, p)

		if got := int(p.SlotCount()); got != len(want) {
			t.Fatalf("SlotCount = %d, want %d", got, len(want))
		}
		if got := p.DeadBytes(); got != 0 {
			t.Errorf("DeadBytes = %d, want 0", got)
		}
		for k, v := range want {
			idx, found, ok := p.SeekSlot([]byte(k))
			if !ok || !found {
				t.Fatalf("SeekSlot(%q) = (%d, %t, %t), want found", k, idx, found, ok)
			}
			if got := string(p.valueAt(idx)); got != v {
				t.Errorf("value of %q = %q, want %q", k, got, v)
			}
		}
	})
}

// ---------- compact-before-split in Insert ----------

func TestInsertCompaction(t *testing.T) {
	t.Run("compacts instead of splitting", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p)
		freeAfterFill := p.freeSpace() // 1056 on 8 KiB, 312 on 4 KiB
		wantErr(t, "Delete", p.Delete([]byte("k0001"), testEnv()), nil)

		key := []byte("z")
		wantErr(t, "Insert", p.Insert(key, maxVal(key), testEnv()), nil)

		if got := p.DeadBytes(); got != 0 {
			t.Errorf("DeadBytes = %d, want 0 (compaction should have run)", got)
		}
		if got := p.freeSpace(); got != freeAfterFill {
			t.Errorf("freeSpace = %d, want %d", got, freeAfterFill)
		}
		mustCheck(t, p)
	})

	t.Run("nil scratch", func(t *testing.T) {
		p := pageNeedsCompact(t)
		free, dead := p.freeSpace(), p.DeadBytes()
		key := []byte("z")
		wantErr(t, "Insert", p.Insert(key, maxVal(key), nil), ErrNoScratch)
		wantUnchanged(t, p, free, dead)
		mustCheck(t, p)
	})

	t.Run("page itself as scratch", func(t *testing.T) {
		p := pageNeedsCompact(t)
		free, dead := p.freeSpace(), p.DeadBytes()
		key := []byte("z")
		wantErr(t, "Insert", p.Insert(key, maxVal(key), &Env{Scratch: p}), ErrNoScratch)
		wantUnchanged(t, p, free, dead)
		mustCheck(t, p)
	})
}

// ---------- in-place and compact-before-split in Update ----------

func TestUpdateCompaction(t *testing.T) {
	t.Run("same-size update needs no space", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p) // free < one max record, dead = 0
		free := p.freeSpace()

		key := []byte("k0000")
		wantErr(t, "Update", p.Update(key, maxVal(key), nil), nil) // nil scratch: proves no compaction

		wantUnchanged(t, p, free, 0)
		idx, _, _ := p.SeekSlot(key)
		if !bytes.Equal(p.valueAt(idx), maxVal(key)) {
			t.Errorf("value of k0000 not updated")
		}
		mustCheck(t, p)
	})

	t.Run("smaller update goes in place", func(t *testing.T) {
		p := newLeaf(t)
		wantErr(t, "Insert", p.Insert([]byte("d"), []byte("wxyz"), nil), nil)
		heap := p.HeapStart()

		wantErr(t, "Update", p.Update([]byte("d"), []byte("ab"), nil), nil)

		if got := p.HeapStart(); got != heap {
			t.Errorf("HeapStart = %d, want %d (no new copy)", got, heap)
		}
		if got := p.DeadBytes(); got != 2 {
			t.Errorf("DeadBytes = %d, want 2 (the leftover tail)", got)
		}
		if got := string(p.valueAt(0)); got != "ab" {
			t.Errorf("valueAt(0) = %q, want %q", got, "ab")
		}
		mustCheck(t, p)
	})

	t.Run("growing update compacts instead of splitting", func(t *testing.T) {
		p := pageGrowA(t)
		free := p.freeSpace()

		key := []byte("a")
		wantErr(t, "Update", p.Update(key, maxVal(key), testEnv()), nil)

		if got := p.DeadBytes(); got != 11 {
			t.Errorf("DeadBytes = %d, want 11 (only the old \"a\" record)", got)
		}
		if got := p.freeSpace(); got != free {
			t.Errorf("freeSpace = %d, want %d", got, free)
		}
		idx, _, _ := p.SeekSlot(key)
		if !bytes.Equal(p.valueAt(idx), maxVal(key)) {
			t.Errorf("value of a not updated")
		}
		mustCheck(t, p)
	})

	t.Run("growing update with nil scratch", func(t *testing.T) {
		p := pageGrowA(t)
		free, dead := p.freeSpace(), p.DeadBytes()
		key := []byte("a")
		wantErr(t, "Update", p.Update(key, maxVal(key), nil), ErrNoScratch)
		wantUnchanged(t, p, free, dead)
		mustCheck(t, p)
	})

	t.Run("growing update with page itself as scratch", func(t *testing.T) {
		p := pageGrowA(t)
		free, dead := p.freeSpace(), p.DeadBytes()
		key := []byte("a")
		wantErr(t, "Update", p.Update(key, maxVal(key), &Env{Scratch: p}), ErrNoScratch)
		wantUnchanged(t, p, free, dead)
		mustCheck(t, p)
	})
}
