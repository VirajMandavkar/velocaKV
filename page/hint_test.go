package page

import "testing"

func TestInsertHint(t *testing.T) {
	p := newLeaf(t)
	env := &Env{Scratch: newTestPage()}

	for _, c := range []struct {
		key  string
		want uint8
		why  string
	}{
		{"m", hintNone, "first key: no pattern yet"},
		{"n", hintEnd, "after the last key"},
		{"o", hintEnd, "after the last key again"},
		{"a", hintFront, "before the first key"},
		{"b", hintNone, "in the middle"},
		{"z", hintEnd, "After the last key"},
	} {
		if err := p.Insert([]byte(c.key), []byte("v"), env); err != nil {
			t.Fatalf("Insert(%q): %v", c.key, err)
		}
		if got := p.hint(); got != c.want {
			t.Fatalf("After Insert(%q) (%s): hint %#x, want %#x", c.key, c.why, got, c.want)
		}
		if err := p.Check(); err != nil {
			t.Fatalf("Check after Insert(%q):  %v", c.key, err)
		}
	}

	if err := p.Update([]byte("m"), []byte("a longer value"), env); err != nil {
		t.Fatal(err)
	}
	if got := p.hint(); got != hintEnd {
		t.Fatalf("after Update: hint %#x, want hintEnd (unchanged)", got)
	}
	if err := p.Delete([]byte("a"), env); err != nil {
		t.Fatal(err)
	}
	if got := p.hint(); got != hintEnd {
		t.Fatalf("after Delete: hint %#x, want hintEnd (unchanged)", got)
	}
}

// The hint lives in the same word as the slot count and must not disturb
// the other fields there.
func TestInsertHintKeepsOtherFields(t *testing.T) {
	p := newLeaf(t)
	p.SetGen(7)
	kind := p.Kind()
	env := &Env{Scratch: newTestPage()}
	for _, k := range []string{"a", "b", "c"} {
		if err := p.Insert([]byte(k), []byte("v"), env); err != nil {
			t.Fatal(err)
		}
	}
	if p.Gen() != 7 || p.Kind() != kind || p.SlotCount() != 3 {
		t.Fatalf("gen %d, kind %d, slots %d; want 7, %d, 3", p.Gen(), p.Kind(), p.SlotCount(), kind)
	}
}
