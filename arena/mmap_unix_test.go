package arena

import "testing"

func TestMapSlab(t *testing.T) {
	b, err := mapSlab()
	if err != nil {
		t.Fatalf("mapSlab got %v, want nil", err)
	}
	t.Cleanup(func() { _ = unmapSlab(b) })
	if len(b) != slabBytes {
		t.Fatalf("mapSlab length of mapped slab got %d, want %d", len(b), slabBytes)
	}
	b[0] = 0xAB
	b[slabBytes-1] = 0xBA

	if got := b[0]; got != 0xAB {
		t.Fatalf("byte 0 = %#x, want 0xAB", got)
	}
	if got := b[slabBytes-1]; got != 0xBA {
		t.Fatalf("byte %d = %#x, want 0xBA", slabBytes-1, got)
	}

	if err := unmapSlab(b); err != nil {
		t.Fatalf("unmap err got %v, want nil", err)
	}
	if err := unmapSlab(b); err == nil {
		t.Fatal("2nd unmap err got nil , want non-nil")
	}
}
