package page

import (
	"bytes"
	"fmt"
	"testing"
)

func TestCompact(t *testing.T) {
	t.Run("reclaim dead bytes", func(t *testing.T) {
		p := pageBDF(t)
		wantErr(t, "Delete", p.Delete([]byte("d")), nil)
		initFreeSpace := p.freeSpace()
		p.Compact(newTestPage())
		mustCheck(t, p)
		if got := p.DeadBytes(); got != 0 {
			t.Fatalf("Compact() Deadbytes got %d want 0", got)
		}
		if got := p.HeapStart(); got != PageSize-4 {
			t.Fatalf("Compact() HeapStart got %d want %d", got, PageSize-4)
		}
		if got := p.freeSpace(); got != initFreeSpace+2 {
			t.Fatalf("COmpact() FreeSpace got %d want %d", got, initFreeSpace+2)
		}
		if got := string(p.valueAt(0)); got != "v" {
			t.Fatalf("Compact() ValueAt 0 got %q want v", got)
		}
		if got := string(p.valueAt(1)); got != "v" {
			t.Fatalf("Compact() ValueAt 1 got %q want v", got)
		}
		if _, got, _ := p.SeekSlot([]byte("b")); got != true {
			t.Fatalf("Compact() SeekSlot(b) got %t want true", got)
		}
		if _, got, _ := p.SeekSlot([]byte("f")); got != true {
			t.Fatalf("Compact() SeekSlot(f) got %t want true", got)
		}
	})

	t.Run("no-op on a clean page", func(t *testing.T) {
		p := pageBDF(t)
		initFreeSpace := p.freeSpace()
		initHeapStart := p.HeapStart()
		p.Compact(newTestPage())
		mustCheck(t, p)
		if got := p.DeadBytes(); got != 0 {
			t.Fatalf("Compact() Deadbytes got %d want 0", got)
		}
		if got := p.HeapStart(); got != initHeapStart {
			t.Fatalf("Compact() HeapStart got %d want %d", got, initHeapStart)
		}
		if got := p.freeSpace(); got != initFreeSpace {
			t.Fatalf("Compact() FreeSpace got %d want %d", got, initFreeSpace)
		}
	})

	t.Run("keep gen", func(t *testing.T) {
		p := pageBDF(t)
		p.SetGen(7)
		wantErr(t, "Delete", p.Delete([]byte("d")), nil)
		p.Compact(newTestPage())
		mustCheck(t, p)
		if got := p.Gen(); got != 7 {
			t.Fatalf("Compact() Gen got %d want 7", got)
		}
	})
	t.Run("content preserved", func(t *testing.T) {
		p := newLeaf(t)
		want := make(map[string]string)

		for i := 0; i < 20; i++ {
			key := fmt.Appendf(nil, "key_%02d", i)
			val := fmt.Appendf(nil, "val_%02d", i)
			wantErr(t, "Insert", p.Insert(key, val, newTestPage()), nil)
			want[string(key)] = string(val) // mirror the insert
		}
		for i := 1; i < 20; i += 2 {
			key := fmt.Appendf(nil, "key_%02d", i)
			wantErr(t, "Delete", p.Delete(key), nil)
			delete(want, string(key)) // mirror the delete
		}

		p.Compact(newTestPage()) // ← the operation under test
		mustCheck(t, p)

		if got := int(p.SlotCount()); got != len(want) {
			t.Fatalf("SlotCount = %d, want %d", got, len(want))
		}
		if got := p.DeadBytes(); got != 0 {
			t.Fatalf("DeadBytes = %d, want 0", got)
		}
		for k, v := range want {
			idx, found, ok := p.SeekSlot([]byte(k))
			if !ok || !found {
				t.Fatalf("SeekSlot(%q) = (%d, %t, %t), want found", k, idx, found, ok)
			}
			if got := string(p.valueAt(idx)); got != v {
				t.Fatalf("value of %q = %q, want %q", k, got, v)
			}
		}
	})
	t.Run("compacts instead of splitting", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p)
		freeAfterFill := p.freeSpace()
		wantErr(t, "Delete()", p.Delete([]byte("k0001")), nil)
		wantErr(t, "Insert()", p.Insert([]byte("z"), make([]byte, MaxInlineRecord-1), newTestPage()), nil)
		if got := p.DeadBytes(); got != 0 {
			t.Errorf("DeadBytes = %d, want 0 (compaction should have run)", got)
		}
		if got := p.freeSpace(); got != freeAfterFill {
			t.Errorf("freeSpace = %d, want %d", got, freeAfterFill)
		}
		mustCheck(t, p)
	})

	t.Run("nil scratch when compaction is needed", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p)
		wantErr(t, "Delete", p.Delete([]byte("k0001")), nil)
		freeBefore, deadBefore := p.freeSpace(), p.DeadBytes()

		key := []byte("z")
		val := make([]byte, MaxInlineRecord-len(key))
		wantErr(t, "Insert", p.Insert(key, val, nil), ErrNoScratch)

		if p.freeSpace() != freeBefore || p.DeadBytes() != deadBefore {
			t.Errorf("page changed after rejected insert: free %d→%d, dead %d→%d",
				freeBefore, p.freeSpace(), deadBefore, p.DeadBytes())
		}
		mustCheck(t, p)
	})

	t.Run("compacts instead of splitting", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p)
		wantErr(t, "Delete", p.Delete([]byte("k0001")), nil)
		freeAfterDelete := p.freeSpace() // 1064 on 8 KiB, 320 on 4 KiB

		key := []byte("k0000")
		val := bytes.Repeat([]byte("u"), MaxInlineRecord-len(key))
		wantErr(t, "Update", p.Update(key, val, newTestPage()), nil)

		if got := int(p.DeadBytes()); got != MaxInlineRecord {
			t.Errorf("DeadBytes = %d, want %d (only the old k0000 copy)", got, MaxInlineRecord)
		}
		if got := p.freeSpace(); got != freeAfterDelete {
			t.Errorf("freeSpace = %d, want %d", got, freeAfterDelete)
		}
		idx, found, ok := p.SeekSlot(key)
		if !ok || !found {
			t.Fatalf("SeekSlot(k0000) = (%d, %t, %t), want found", idx, found, ok)
		}
		if !bytes.Equal(p.valueAt(idx), val) {
			t.Errorf("value of k0000 not updated")
		}
		mustCheck(t, p)
	})

	t.Run("nil scratch when compaction is needed", func(t *testing.T) {
		p := newLeaf(t)
		fillPage(t, p)
		wantErr(t, "Delete", p.Delete([]byte("k0001")), nil)
		freeBefore, deadBefore := p.freeSpace(), p.DeadBytes()

		key := []byte("k0000")
		val := make([]byte, MaxInlineRecord-len(key))
		wantErr(t, "Update", p.Update(key, val, nil), ErrNoScratch)

		if p.freeSpace() != freeBefore || p.DeadBytes() != deadBefore {
			t.Errorf("page changed after rejected update: free %d→%d, dead %d→%d",
				freeBefore, p.freeSpace(), deadBefore, p.DeadBytes())
		}
		mustCheck(t, p)
	})

}
