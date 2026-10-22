package page

import (
	"bytes"
	"testing"
)

type rec struct {
	key    string
	valLen int
}

// leafWith returns a leaf (no fences) holding recs, inserted in order.
func leafWith(t *testing.T, recs ...rec) *Page {
	t.Helper()
	p := newLeaf(t)
	env := &Env{Scratch: newTestPage(), Pages: newMemPages()}
	for _, r := range recs {
		if err := p.Insert([]byte(r.key), make([]byte, r.valLen), env); err != nil {
			t.Fatalf("Insert(%q, %d bytes): %v", r.key, r.valLen, err)
		}
	}
	return p
}

func TestSplitPoint(t *testing.T) {
	small := func(k string) rec { return rec{k, 10} }

	for _, c := range []struct {
		name string
		recs []rec
		want int
	}{
		{
			name: "Equal records split in half",
			recs: []rec{small("k0"), small("k1"), small("k2"), small("k3")},
			want: 2,
		},
		{
			name: "big record last goes right alone",
			recs: []rec{small("k0"), small("k1"), small("k2"), {"z", 700}},
			want: 3,
		},
		{
			// 709 first: 2·709 ≥ 769 at slot 0 → m = 1
			name: "big record first stays left alone",
			recs: []rec{{"a", 700}, small("k1"), small("k2"), small("k3")},
			want: 1,
		},
		{
			name: "overflow record counts as its stub",
			recs: []rec{small("k0"), small("k1"), small("k2"), {"z", 3000}},
			want: 2,
		},
		{
			name: "two records",
			recs: []rec{small("k0"), {"z", 700}},
			want: 1,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := leafWith(t, c.recs...)
			if got := p.SplitPoint(); got != c.want {
				t.Fatalf("SplitPoint = %d, want %d", got, c.want)
			}
		})
	}
}

func TestKeyAt(t *testing.T) {
	p := newTestPage()
	if err := p.Format(KindLeaf, []byte("key-0040"), []byte("key-0050")); err != nil {
		t.Fatal(err)
	}
	if got := string(p.Prefix()); got != "key-00" {
		t.Fatalf("prefix = %q, want %q", got, "key-00")
	}
	env := &Env{Scratch: newTestPage()}
	if err := p.Insert([]byte("42"), []byte("v"), env); err != nil { // the page stores the suffix
		t.Fatal(err)
	}

	if got := p.KeyAt(0, nil); !bytes.Equal(got, []byte("key-0042")) {
		t.Fatalf("KeyAt(0, nil) = %q, want %q", got, "key-0042")
	}
	// It appends: existing bytes in dst are kept.
	if got := p.KeyAt(0, []byte("x:")); !bytes.Equal(got, []byte("x:key-0042")) {
		t.Fatalf("KeyAt(0, \"x:\") = %q, want %q", got, "x:key-0042")
	}
}
