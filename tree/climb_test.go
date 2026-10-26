package tree

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

func key11(i int) []byte { return fmt.Appendf(nil, "key-%07d", i) }

// height returns the number of levels: inner pages on a path + the leaf.
func height(t *testing.T, w *Worker) int {
	t.Helper()
	if _, _, err := w.descend([]byte("key-")); err != nil {
		t.Fatalf("descend: %v", err)
	}
	return w.depth + 1
}

// runPuts inserts keys in the given order, running checkTree after every
// put for the first 2000, then every checkEvery puts, and at the end.
// Then it reads every key back.
func runPuts(t *testing.T, w *Worker, keys [][]byte, val func(i int) []byte, checkEvery int) {
	t.Helper()
	for n, k := range keys {
		if err := w.Put(k, val(n)); err != nil {
			t.Fatalf("Put #%d (%.20q…): %v", n, k, err)
		}
		if n < 2000 || (n+1)%checkEvery == 0 {
			if err := checkTree(w, 1); err != nil {
				t.Fatalf("checkTree after put #%d (%.20q…): %v", n, k, err)
			}
		}
	}
	if err := checkTree(w, 1); err != nil {
		t.Fatalf("checkTree at end: %v", err)
	}
	for n, k := range keys {
		got, err := w.Get(k, nil)
		if err != nil || !bytes.Equal(got, val(n)) {
			t.Fatalf("Get #%d (%.20q…): %d bytes, %v", n, k, len(got), err)
		}
	}
}

const checkEvery = 500

func wantHeight(t *testing.T, w *Worker, min int) {
	t.Helper()
	h := height(t, w)
	t.Logf("height %d, live pages %d", h, w.t.a.LivePages())
	if h < min {
		t.Fatalf("height %d, want at least %d (no inner split happened)", h, min)
	}
}

func TestClimbSequential(t *testing.T) {
	_, w := newOpsWorker(t)
	keys := make([][]byte, 100_000)
	for i := range keys {
		keys[i] = key11(i)
	}
	runPuts(t, w, keys, func(int) []byte { return val16 }, checkEvery)
	wantHeight(t, w, 3)
}

func TestClimbReverse(t *testing.T) {
	_, w := newOpsWorker(t)
	keys := make([][]byte, 100_000)
	for i := range keys {
		keys[i] = key11(len(keys) - 1 - i)
	}
	runPuts(t, w, keys, func(int) []byte { return val16 }, checkEvery)
	wantHeight(t, w, 3)
}

func TestClimbRandom(t *testing.T) {
	_, w := newOpsWorker(t)
	r := rand.New(rand.NewPCG(1, 2)) // fixed seed: the same order every run
	perm := r.Perm(100_000)
	keys := make([][]byte, len(perm))
	for n, i := range perm {
		keys[n] = key11(i)
	}
	runPuts(t, w, keys, func(int) []byte { return val16 }, checkEvery)
	wantHeight(t, w, 3)

	// Overwrite every 3rd key after all the splits, then read everything.
	newVal := []byte("updated-value-16")
	for n := 0; n < len(keys); n += 3 {
		if err := w.Put(keys[n], newVal); err != nil {
			t.Fatalf("overwrite %q: %v", keys[n], err)
		}
	}
	if err := checkTree(w, 1); err != nil {
		t.Fatalf("checkTree after overwrites: %v", err)
	}
	for n, k := range keys {
		want := val16
		if n%3 == 0 {
			want = newVal
		}
		mustGet(t, w, k, want)
	}
}

func TestClimbLongSeparators(t *testing.T) {
	_, w := newOpsWorker(t)
	pad := strings.Repeat("a", 400)
	keys := make([][]byte, 5_000)
	for i := range keys {
		keys[i] = fmt.Appendf(nil, "%s%07d", pad, i) // 407 bytes; differ only at the end
	}
	runPuts(t, w, keys, func(int) []byte { return val16 }, checkEvery)
	wantHeight(t, w, 3)
}

func TestClimbOverflowValues(t *testing.T) {
	_, w := newOpsWorker(t)
	r := rand.New(rand.NewPCG(3, 4))
	perm := r.Perm(4_000)
	keys := make([][]byte, len(perm))
	for n, i := range perm {
		keys[n] = key11(i)
	}
	val := func(n int) []byte {
		if n%7 == 0 {
			v := make([]byte, 3000) // overflow on both builds
			for j := range v {
				v[j] = byte(n + j)
			}
			return v
		}
		return val16
	}
	runPuts(t, w, keys, val, 1) // checkTree after every put: stubs must never leak or double up
	t.Logf("height %d, live pages %d", height(t, w), w.t.a.LivePages())
}
