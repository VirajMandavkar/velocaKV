package arena

import (
	"testing"
	"velocakv/page"
)

func TestMapSlab(t *testing.T) {
	b, err := mapSlab()
	if err != nil {
		t.Fatalf("mapSlab got %v, want nil", err)
	}
	t.Cleanup(func() { _ = unmapSlab(b) })
	if len(b) != page.SlabBytes {
		t.Fatalf("mapSlab length of mapped slab got %d, want %d", len(b), page.SlabBytes)
	}
	b[0] = 0xAB
	b[page.SlabBytes-1] = 0xBA

	if got := b[0]; got != 0xAB {
		t.Fatalf("byte 0 = %#x, want 0xAB", got)
	}
	if got := b[page.SlabBytes-1]; got != 0xBA {
		t.Fatalf("byte %d = %#x, want 0xBA", page.SlabBytes-1, got)
	}

	if err := unmapSlab(b); err != nil {
		t.Fatalf("unmap err got %v, want nil", err)
	}
	if err := unmapSlab(b); err == nil {
		t.Fatal("2nd unmap err got nil , want non-nil")
	}
}

func TestSplit(t *testing.T) {
	P := page.PagesPerSlab
	S := page.PageSize
	cases := []struct {
		id, wantSlab, wantOff int
	}{
		{0, 0, 0},
		{1, 0, S},
		{P - 1, 0, (P - 1) * S},
		{P, 1, 0},
		{P + 3, 1, 3 * S},
	}

	for _, c := range cases {
		gotSlab, gotOff := split(uint32(c.id))
		if gotSlab != c.wantSlab || gotOff != c.wantOff {
			t.Errorf("Split(%d) gotSlab: %d, gotOff: %d; wantSlab: %d, wantOff: %d", c.id, gotSlab, gotOff, c.wantSlab, c.wantOff)
		}
	}
}
