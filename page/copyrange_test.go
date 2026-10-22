package page

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"testing"
)

// The prefix grows from 0 to 7, so every suffix is re-cut from 8 bytes to 1.
func TestCopyRangePrefixGrows(t *testing.T) {
	env := &Env{Scratch: newTestPage()}
	p := newLeaf(t) // unfenced: prefix 0, suffix = whole key
	for i := 40; i < 50; i++ {
		k, v := fmt.Sprintf("key-%04d", i), fmt.Sprintf("v-%d", i)
		if err := p.Insert([]byte(k), []byte(v), env); err != nil {
			t.Fatalf("Insert(%q): %v", k, err)
		}
	}

	dst := newTestPage()
	if err := dst.Format(KindLeaf, []byte("key-0045"), []byte("key-0049")); err != nil {
		t.Fatal(err)
	}
	if got := dst.PrefixLen(); got != 7 { // lcp("key-0045", "key-0049") = "key-004"
		t.Fatalf("dst prefix = %d, want 7", got)
	}

	if err := p.CopyRange(dst, 5, 9); err != nil { // key-0045 … key-0048
		t.Fatalf("CopyRange: %v", err)
	}
	if n := dst.SlotCount(); n != 4 {
		t.Fatalf("dst slots = %d, want 4", n)
	}
	for j := range 4 {
		i := 45 + j
		wantKey, wantVal := fmt.Sprintf("key-%04d", i), fmt.Sprintf("v-%d", i)

		if got := dst.KeyAt(j, nil); string(got) != wantKey {
			t.Errorf("KeyAt(%d) = %q, want %q", j, got, wantKey)
		}
		if kl := dst.slotAt(j).keyLen(); kl != 1 {
			t.Errorf("slot %d stored suffix = %d bytes, want 1", j, kl)
		}
		got, err := dst.Get([]byte(wantKey)[7:], nil, env)
		if err != nil || string(got) != wantVal {
			t.Errorf("Get(%q) = %q, %v; want %q", wantKey, got, err, wantVal)
		}
	}
	if err := dst.Check(); err != nil {
		t.Fatalf("dst.Check: %v", err)
	}
}

// An overflow record moves as its 8-byte stub: same chain, no new pages.
func TestCopyRangeMovesOverflowStub(t *testing.T) {
	src := newMemPages()
	env := &Env{Scratch: newTestPage(), Pages: src}

	big := make([]byte, 3000)
	for i := range big {
		big[i] = byte(i % 251) // a pattern, so reordered bytes would show
	}
	p := newLeaf(t)
	for _, r := range []struct {
		k string
		v []byte
	}{{"a", []byte("small")}, {"big", big}, {"c", []byte("small")}} {
		if err := p.Insert([]byte(r.k), r.v, env); err != nil {
			t.Fatalf("Insert(%q): %v", r.k, err)
		}
	}
	idsBefore, err := p.AppendOverflowIDs(nil, src)
	if err != nil {
		t.Fatal(err)
	}
	pagesBefore := len(src.pages)

	dst := newLeaf(t)
	if err := p.CopyRange(dst, 0, 3); err != nil {
		t.Fatalf("CopyRange: %v", err)
	}

	if !dst.slotAt(1).ovf() {
		t.Fatal("slot 1 lost its OVF flag")
	}
	if n := len(src.pages); n != pagesBefore {
		t.Fatalf("pages allocated: %d → %d, want no change", pagesBefore, n)
	}
	idsAfter, err := dst.AppendOverflowIDs(nil, src)
	if err != nil || !slices.Equal(idsAfter, idsBefore) {
		t.Fatalf("dst chain = %v, %v; want %v (the same pages)", idsAfter, err, idsBefore)
	}
	got, err := dst.Get([]byte("big"), nil, env)
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("Get(big): %d bytes, %v; want the original 3000", len(got), err)
	}
}

// A full dst refuses the record and stays valid.
func TestCopyRangeNoRoom(t *testing.T) {
	env := &Env{Scratch: newTestPage()}
	dst := newLeaf(t)
	val := make([]byte, 700) // inline on both builds
	for i := 0; ; i++ {
		err := dst.Insert(fmt.Appendf(nil, "%04d", i), val, env) // 4 + 700 + 8 = 712 bytes each
		if errors.Is(err, ErrNeedSplit) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	before := dst.SlotCount()

	p := newLeaf(t)
	if err := p.Insert([]byte("zzzz"), val, env); err != nil { // same 712 bytes; sorts after dst's keys
		t.Fatal(err)
	}
	if err := p.CopyRange(dst, 0, 1); !errors.Is(err, ErrNeedSplit) {
		t.Fatalf("CopyRange into a full page = %v, want ErrNeedSplit", err)
	}
	if n := dst.SlotCount(); n != before {
		t.Fatalf("dst slots %d → %d after a refused copy", before, n)
	}
	if err := dst.Check(); err != nil {
		t.Fatalf("dst.Check after refusal: %v", err)
	}
}

func TestCopyRangeRejectsBadTarget(t *testing.T) {
	p := newTestPage()
	if err := p.Format(KindLeaf, []byte("key-0045"), []byte("key-0049")); err != nil { // prefix 7
		t.Fatal(err)
	}

	shorter := newLeaf(t) // prefix 0 < 7: the suffixes cannot be re-cut
	if err := p.CopyRange(shorter, 0, 0); !errors.Is(err, ErrPageInvariant) {
		t.Errorf("into shorter prefix = %v, want ErrPageInvariant", err)
	}

	inner := newTestPage()
	if err := inner.Format(KindInner, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.CopyRange(inner, 0, 0); !errors.Is(err, ErrPageInvariant) {
		t.Errorf("leaf into inner = %v, want ErrPageInvariant", err)
	}
}
