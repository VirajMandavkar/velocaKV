package tree

import (
	"bytes"
	"fmt"
	"sort"
	"testing"

	"velocakv/page"
)

// scanAll runs Scan and returns copies of every key and value it gave.
func scanAll(t *testing.T, w *Worker, start, end []byte) (keys, vals [][]byte) {
	t.Helper()
	err := w.Scan(start, end, func(k, v []byte) bool {
		keys = append(keys, bytes.Clone(k)) // k, v are only valid during the call
		vals = append(vals, bytes.Clone(v))
		return true
	})
	if err != nil {
		t.Fatalf("Scan(%q, %q): %v", start, end, err)
	}
	return keys, vals
}

// refRange returns the keys of the sorted reference list that fall in [start, end).
func refRange(ref [][]byte, start, end []byte) [][]byte {
	lo := 0
	if start != nil {
		lo = sort.Search(len(ref), func(i int) bool { return bytes.Compare(ref[i], start) >= 0 })
	}
	hi := len(ref)
	if end != nil {
		hi = sort.Search(len(ref), func(i int) bool { return bytes.Compare(ref[i], end) >= 0 })
	}
	if lo > hi {
		return nil
	}
	return ref[lo:hi]
}

func sameKeys(t *testing.T, what string, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d keys, want %d", what, len(got), len(want))
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("%s: key #%d = %q, want %q", what, i, got[i], want[i])
		}
	}
}

func scanVal(i int) []byte { return fmt.Appendf(nil, "v%d", i) }

// loadSequential puts key11(0 … n-1) and returns the sorted reference list.
func loadSequential(t *testing.T, w *Worker, n int) [][]byte {
	t.Helper()
	ref := make([][]byte, n)
	for i := range n {
		ref[i] = key11(i)
		if err := w.Put(ref[i], scanVal(i)); err != nil {
			t.Fatalf("Put(%q): %v", ref[i], err)
		}
	}
	return ref
}

// Random insert order, scanned back: every key once, in order, with its value.
func TestScanFullRandom(t *testing.T) {
	_, w := newOpsWorker(t)
	const n = 50_000
	ref := make([][]byte, 0, n)
	for i := range n {
		k, x := fillKey(i)
		if err := w.Put(k, scanVal(x)); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
		ref = append(ref, k)
	}
	sort.Slice(ref, func(i, j int) bool { return bytes.Compare(ref[i], ref[j]) < 0 })

	keys, vals := scanAll(t, w, nil, nil)
	sameKeys(t, "Scan(nil, nil)", keys, ref)
	for i, k := range keys {
		var x int
		fmt.Sscanf(string(k), "key-%d", &x)
		if !bytes.Equal(vals[i], scanVal(x)) {
			t.Fatalf("value of %q = %q, want %q", k, vals[i], scanVal(x))
		}
	}
}

// Bounds: nil, inside the data, between keys, empty, reversed, and exactly
// on a page boundary (the root's separator is some leaf's high fence).
func TestScanBounds(t *testing.T) {
	tr, w := newOpsWorker(t)
	ref := loadSequential(t, w, 20_000)

	root, err := load(w.aw, desc(tr.root.Load()))
	if err != nil {
		t.Fatal(err)
	}
	if root.Kind() != page.KindInner {
		t.Fatal("tree did not split: no page boundary to test")
	}
	boundary := root.KeyAt(0, nil) // a leaf's high fence and its right neighbour's low fence

	between := append(key11(100), 'x') // after key-0000100, before key-0000101
	cases := []struct {
		name       string
		start, end []byte
	}{
		{"everything", nil, nil},
		{"from nil", nil, key11(5000)},
		{"to nil", key11(15000), nil},
		{"inside", key11(100), key11(200)},
		{"start between keys", between, key11(110)},
		{"end between keys", key11(90), between},
		{"empty: start == end", key11(150), key11(150)},
		{"empty: start > end", key11(200), key11(100)},
		{"end on a page boundary", nil, boundary},
		{"start on a page boundary", boundary, nil},
		{"one page boundary inside", key11(0), append(bytes.Clone(boundary), 'x')},
		{"below all keys", nil, []byte("a")},
		{"above all keys", []byte("z"), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keys, _ := scanAll(t, w, c.start, c.end)
			sameKeys(t, c.name, keys, refRange(ref, c.start, c.end))
		})
	}
}

// fn returning false stops the scan after exactly that record.
func TestScanEarlyStop(t *testing.T) {
	_, w := newOpsWorker(t)
	loadSequential(t, w, 5000)
	calls := 0
	if err := w.Scan(nil, nil, func(k, v []byte) bool {
		calls++
		return calls < 10
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 10 {
		t.Fatalf("fn called %d times, want 10", calls)
	}
}

// Overflow values come back whole, across leaf boundaries.
func TestScanOverflowValues(t *testing.T) {
	_, w := newOpsWorker(t)
	big := func(i int) []byte {
		v := make([]byte, 3000)
		for j := range v {
			v[j] = byte(i + j)
		}
		return v
	}
	const n = 2000
	for i := range n {
		v := scanVal(i)
		if i%5 == 0 {
			v = big(i)
		}
		if err := w.Put(key11(i), v); err != nil {
			t.Fatal(err)
		}
	}
	keys, vals := scanAll(t, w, nil, nil)
	if len(keys) != n {
		t.Fatalf("%d keys, want %d", len(keys), n)
	}
	for i := range n {
		want := scanVal(i)
		if i%5 == 0 {
			want = big(i)
		}
		if !bytes.Equal(vals[i], want) {
			t.Fatalf("value %d: %d bytes, want %d", i, len(vals[i]), len(want))
		}
	}
}

// Deleting a whole run of keys leaves empty leaves (no merges): Scan must
// walk straight past them.
func TestScanSkipsEmptyLeaves(t *testing.T) {
	_, w := newOpsWorker(t)
	ref := loadSequential(t, w, 20_000)
	for i := 1000; i < 9000; i++ {
		if err := w.Delete(key11(i)); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}
	ref = append(ref[:1000:1000], ref[9000:]...)

	for _, c := range []struct{ start, end []byte }{
		{nil, nil},
		{key11(500), key11(9500)},  // spans the emptied region
		{key11(2000), key11(8000)}, // entirely inside it
	} {
		keys, _ := scanAll(t, w, c.start, c.end)
		sameKeys(t, fmt.Sprintf("[%q, %q)", c.start, c.end), keys, refRange(ref, c.start, c.end))
	}
}

// After the value buffer has grown once, scanning allocates nothing.
func TestScanAllocs(t *testing.T) {
	_, w := newOpsWorker(t)
	loadSequential(t, w, 5000)
	count := 0
	fn := func(k, v []byte) bool { count++; return true }

	if err := w.Scan(nil, nil, fn); err != nil { // warm-up: valBuf grows here
		t.Fatal(err)
	}
	start, end := key11(100), key11(4000) // allocated once, outside the measurement
	allocs := testing.AllocsPerRun(10, func() {
		if err := w.Scan(start, end, fn); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("Scan allocated %v times per call, want 0", allocs)
	}
}

// Range gives the same keys as Scan, supports break, and leaves Err nil.
func TestRange(t *testing.T) {
	_, w := newOpsWorker(t)
	ref := loadSequential(t, w, 3000)

	var got [][]byte
	for k := range w.Range(key11(10), key11(2500)) {
		got = append(got, bytes.Clone(k))
	}
	if err := w.Err(); err != nil {
		t.Fatalf("Err after Range: %v", err)
	}
	sameKeys(t, "Range", got, refRange(ref, key11(10), key11(2500)))

	n := 0
	for range w.Range(nil, nil) {
		n++
		if n == 7 {
			break // becomes yield → false inside Scan
		}
	}
	if n != 7 {
		t.Fatalf("break after %d, want 7", n)
	}
}
