package page

import (
	"bytes"
	"fmt"
	"sync/atomic"
	"testing"
)

func TestAdopt(t *testing.T) {
	env := &Env{Scratch: newTestPage()}

	// p: the page being split. It has its own gen and a held version word,
	// and it used to hold 20 big records, so its future free gap is dirty.
	p := newTestPage()
	if err := p.Format(KindLeaf, nil, nil); err != nil {
		t.Fatal(err)
	}
	old := bytes.Repeat([]byte{0xEE}, 100)
	for i := range 20 {
		if err := p.Insert(fmt.Appendf(nil, "old-%02d", i), old, env); err != nil {
			t.Fatal(err)
		}
	}
	p.SetGen(7)
	atomic.StoreUint64(p.word(0), 0xABC) // version word: owned by locking

	// src: the left half, built in scratch, with a different gen.
	src := newTestPage()
	if err := src.Format(KindLeaf, []byte("key-0040"), []byte("key-0050")); err != nil {
		t.Fatal(err)
	}
	for i := 41; i <= 45; i++ {
		suffix := fmt.Appendf(nil, "%02d", i) // prefix is "key-00"
		if err := src.Insert(suffix, fmt.Appendf(nil, "v%d", i), env); err != nil {
			t.Fatal(err)
		}
	}
	src.SetGen(2)

	p.Adopt(src)

	// 1. What p owns is kept.
	if g := p.Gen(); g != 7 {
		t.Errorf("gen = %d, want 7 (P76: links to p carry gen 7)", g)
	}
	if v := atomic.LoadUint64(p.word(0)); v != 0xABC {
		t.Errorf("version word = %#x, want 0xABC", v)
	}

	// 2. Everything else in the header is src's.
	genMask := uint64(mask32) << shiftGen
	w1p := atomic.LoadUint64(p.word(word1)) &^ genMask
	w1s := atomic.LoadUint64(src.word(word1)) &^ genMask
	if w1p != w1s {
		t.Errorf("word 1 without gen = %#x, want %#x", w1p, w1s)
	}
	if !bytes.Equal(p[16:HeaderSize], src[16:HeaderSize]) {
		t.Error("header words 2–7 differ from src")
	}

	// 3. The page reads exactly like src.
	if p.Kind() != KindLeaf || p.SlotCount() != 5 || p.PrefixLen() != src.PrefixLen() {
		t.Fatalf("kind %d, slots %d, prefix %d; want leaf, 5, %d",
			p.Kind(), p.SlotCount(), p.PrefixLen(), src.PrefixLen())
	}
	ph, pinf := p.HighFence()
	sh, sinf := src.HighFence()
	if !bytes.Equal(p.LowFence(), src.LowFence()) || !bytes.Equal(ph, sh) || pinf != sinf {
		t.Error("fences differ from src")
	}
	for i := range 5 {
		if got, want := p.KeyAt(i, nil), src.KeyAt(i, nil); !bytes.Equal(got, want) {
			t.Errorf("KeyAt(%d) = %q, want %q", i, got, want)
		}
		suffix := fmt.Appendf(nil, "%02d", 41+i)
		got, err := p.Get(suffix, nil, env)
		if want := fmt.Sprintf("v%d", 41+i); err != nil || string(got) != want {
			t.Errorf("Get(%q) = %q, %v; want %q", suffix, got, err, want)
		}
	}
	if err := p.Check(); err != nil {
		t.Fatalf("p.Check after Adopt: %v", err)
	}

	// 4. The free gap was skipped: p still holds its old bytes there.
	n, hs := slotOff(int(p.SlotCount())), int(p.HeapStart())
	if bytes.Equal(p[n:hs], src[n:hs]) {
		t.Error("free gap matches src: Adopt copied bytes no slot points at")
	}
}
