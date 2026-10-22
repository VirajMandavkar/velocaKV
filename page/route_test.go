package page

import (
	"encoding/binary"
	"testing"
)

func newInner(t *testing.T, upper uint64, seps []string, kids []uint64) *Page {
	t.Helper()
	p := newTestPage()
	if err := p.Format(KindInner, nil, nil); err != nil {
		t.Fatal(err)
	}
	env := &Env{Scratch: newTestPage()}
	for i, sep := range seps {
		val := binary.LittleEndian.AppendUint64(nil, kids[i])
		if err := p.Insert([]byte(sep), val, env); err != nil {
			t.Fatalf("Insert(%q): %v", sep, err)
		}
	}
	p.SetUpperChild(upper)
	return p
}

func TestRoute(t *testing.T) {
	p := newInner(t, 300, []string{"g", "p"}, []uint64{100, 200})
	for _, c := range []struct {
		key  string
		want uint64
	}{
		{"", 100},   // empty key sorts first
		{"a", 100},  // below "g"
		{"g", 200},  // equal to a separator: goes right
		{"gg", 200}, // just above "g"
		{"k", 200},
		{"oz", 200}, // just below "p"
		{"p", 300},  // equal to the last separator: upperChild
		{"z", 300},  // above every separator
	} {
		got, ok := p.Route([]byte(c.key))
		if !ok || got != c.want {
			t.Errorf("Route(%q) = %d, %v; want %d, true", c.key, got, ok, c.want)
		}
	}
}

func TestRouteNoSeparators(t *testing.T) {
	p := newInner(t, 300, nil, nil)
	if got, ok := p.Route([]byte("anything")); !ok || got != 300 {
		t.Fatalf("Route on empty inner = %d, %v; want 300, true", got, ok)
	}
}

func TestRouteRejectsBadRecord(t *testing.T) {
	p := newTestPage()
	if err := p.Format(KindInner, nil, nil); err != nil {
		t.Fatal(err)
	}
	env := &Env{Scratch: newTestPage()}
	if err := p.Insert([]byte("m"), []byte("short"), env); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Route([]byte("a")); ok {
		t.Fatal("Route accepted a 5-byte child value")
	}
}
