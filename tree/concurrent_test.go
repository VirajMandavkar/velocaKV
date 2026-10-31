package tree

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"runtime"
	"slices"
	"sync"
	"testing"
)

// Many readers, each with its own Worker, on a tree nobody is writing
// Run with -race: any shared write on the read path is reported.
func TestConcurrentReaders(t *testing.T) {
	tr := newTestTree(t)
	builder, err := tr.NewWorker()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(builder.Close)

	const n = 50_000
	valueFor := func(x int) []byte {
		if x%97 == 0 {
			v := make([]byte, 3000)
			for j := range v {
				v[j] = byte(x + j)
			}
			return v
		}
		return scanVal(x)
	}
	xs := make([]int, 0, n)
	for i := range n {
		k, x := fillKey(i)
		if err := builder.Put(k, valueFor(x)); err != nil {
			t.Fatal(err)
		}
		xs = append(xs, x)
	}
	slices.Sort(xs)
	if err := checkTree(builder, 1); err != nil {
		t.Fatal(err)
	}
	liveBefore := tr.a.LivePages()

	readers := runtime.GOMAXPROCS(0)
	const ops = 3000
	var wg sync.WaitGroup
	for r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := tr.NewWorker()
			if err != nil {
				t.Errorf("reader %d: NewWorker: %v", r, err)
				return
			}
			defer w.Close()
			rng := rand.New(rand.NewPCG(uint64(r), 7))
			var dst []byte

			for op := range ops {
				switch rng.IntN(3) {
				case 0:
					x := xs[rng.IntN(len(xs))]
					got, err := w.Get(key11(x), dst[:0])
					if err != nil || !bytes.Equal(got, valueFor(x)) {
						t.Errorf("reader %d op %d: Get(%d): %d bytes, %v", r, op, x, len(got), err)
						return
					}
					dst = got
				case 1:
					k, _ := fillKey(n + rng.IntN(10_000))
					if _, err := w.Get(k, dst[:0]); !errors.Is(err, ErrNotFound) {
						t.Errorf("reader %d op %d: Get(missing %q): %v, want ErrNotFound", r, op, k, err)
					}
				case 2:
					i := rng.IntN(len(xs))
					j := min(len(xs), i+1+rng.IntN(200))
					var end []byte
					if j < len(xs) {
						end = key11(xs[j])
					}
					next := i
					err := w.Scan(key11(xs[i]), end, func(k, v []byte) bool {
						if next >= j || !bytes.Equal(k, key11(xs[next])) {
							t.Errorf("reader %d op %d : Scan gave %q at position %d", r, op, k, next-i)
							return false
						}
						next++
						return true
					})
					if err != nil || next != j {
						t.Errorf("reader %d op %d Scan returned %d keys, want %d (err %v)", r, op, next-i, j-i, err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}

	if err := checkTree(builder, 1); err != nil {
		t.Fatalf("checkTree after readers: %v", err)
	}
	if live := tr.a.LivePages(); live != liveBefore {
		t.Fatalf("live Pages %d -> %d: a reader leaked or worte pages", liveBefore, live)
	}
}
