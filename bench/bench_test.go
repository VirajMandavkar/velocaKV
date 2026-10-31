package bench

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"testing"

	"velocakv/engine"
	"velocakv/tree"
)

const arenaBytes = 256 << 20 // 4 slabs: headroom so fill differences can't hit ErrArenaFull

var sizes = []int{1_000, 10_000, 100_000, 1_000_000}

// store is the one interface both engines are driven through. Both pay the
// same interface-call cost (~1–2 ns), so it cancels out in the comparison.
type store interface {
	put(k, v []byte) error
	get(k []byte) (v []byte, ok bool, err error)
	del(k []byte) (ok bool, err error)
	scan(start, end []byte, fn func(k, v []byte) bool) error
	close() error
}

// ---- v2 ----

type v2Store struct{ t *engine.Tree }

func openV2() (store, error) { return &v2Store{engine.NewTree()}, nil }

func (s *v2Store) put(k, v []byte) error { s.t.Put(k, v, 0); return nil }

func (s *v2Store) get(k []byte) ([]byte, bool, error) {
	v, _, ok := s.t.Get(k, 0)
	return v, ok, nil
}

func (s *v2Store) del(k []byte) (bool, error) { return s.t.Delete(k, 0), nil }

func (s *v2Store) scan(start, end []byte, fn func(k, v []byte) bool) error {
	for _, kv := range s.t.Scan(start, end, 0) { // v2 builds the whole result slice first
		if !fn(kv.Key, kv.Val) {
			break
		}
	}
	return nil
}

func (s *v2Store) close() error { s.t.Close(); return nil }

// ---- v3 ----

type v3Store struct {
	t   *tree.Tree
	w   *tree.Worker
	buf []byte
}

func openV3() (store, error) {
	t, err := tree.New(arenaBytes)
	if err != nil {
		return nil, err
	}
	w, err := t.NewWorker()
	if err != nil {
		return nil, errors.Join(err, t.Close())
	}
	return &v3Store{t: t, w: w, buf: make([]byte, 0, 64)}, nil
}

func (s *v3Store) put(k, v []byte) error { return s.w.Put(k, v) }

func (s *v3Store) get(k []byte) ([]byte, bool, error) {
	v, err := s.w.Get(k, s.buf[:0])
	if errors.Is(err, tree.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	s.buf = v // keep a grown buffer: the next Get reuses it
	return v, true, nil
}

func (s *v3Store) del(k []byte) (bool, error) {
	err := s.w.Delete(k)
	if errors.Is(err, tree.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *v3Store) scan(start, end []byte, fn func(k, v []byte) bool) error {
	return s.w.Scan(start, end, fn)
}

func (s *v3Store) close() error {
	s.w.Close()
	return s.t.Close()
}

var impls = []struct {
	name string
	open func() (store, error)
}{{"v2", openV2}, {"v3", openV3}}

// ---- harness ----

// each runs body for every (size, engine). Names come out as n=1000/impl=v2,
// so `benchstat -col /impl` puts v2 and v3 side by side.
func each(b *testing.B, body func(b *testing.B, n int, open func() (store, error))) {
	for _, n := range sizes {
		for _, im := range impls {
			b.Run(fmt.Sprintf("n=%d/impl=%s", n, im.name), func(b *testing.B) {
				body(b, n, im.open)
			})
		}
	}
}

func mustOpen(b *testing.B, open func() (store, error)) store {
	b.Helper()
	s, err := open()
	if err != nil {
		b.Fatal(err)
	}
	return s
}

func mustClose(b *testing.B, s store) {
	b.Helper()
	if err := s.close(); err != nil {
		b.Fatal(err)
	}
}

func makeKeys(n int) [][]byte {
	keys := make([][]byte, n)
	for i := range keys {
		keys[i] = fmt.Appendf(nil, "key-%08d", i)
	}
	return keys
}

// shuffled returns a copy in a fixed pseudo-random order: both engines see
// exactly the same sequence.
func shuffled(keys [][]byte) [][]byte {
	s := append([][]byte(nil), keys...)
	r := rand.New(rand.NewPCG(1, 2))
	r.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
	return s
}

// gcMeter counts GC cycles and total stop-the-world pause across the loop.
// ReadMemStats itself stops the world, so it runs only before and after.
// It also covers untimed rebuilds: treat it as approximate.
type gcMeter struct {
	num   uint32
	pause uint64
}

func startGC() gcMeter {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return gcMeter{m.NumGC, m.PauseTotalNs}
}

func (g gcMeter) report(b *testing.B) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	mops := float64(b.N) / 1e6
	b.ReportMetric(float64(m.NumGC-g.num)/mops, "GCs/Mop")
	b.ReportMetric(float64(m.PauseTotalNs-g.pause)/1e3/mops, "pause-us/Mop")
}

// ---- correctness guards: outside the timer; a lost write fails the run ----

func verify(b *testing.B, s store, keys, vals [][]byte) {
	b.Helper()
	for i, k := range keys {
		v, ok, err := s.get(k)
		if err != nil || !ok || !bytes.Equal(v, vals[i]) {
			b.Fatalf("CORRECTNESS: key %s: ok=%v err=%v", k, ok, err)
		}
	}
}

func verifyGone(b *testing.B, s store, keys [][]byte) {
	b.Helper()
	for _, k := range keys {
		if _, ok, err := s.get(k); err != nil || ok {
			b.Fatalf("CORRECTNESS: deleted key %s: ok=%v err=%v", k, ok, err)
		}
	}
}

// verifyScan: one full scan, keys strictly increasing, exact count.
// The bounds are explicit because v2's Scan returns nothing for end == nil.
func verifyScan(b *testing.B, s store, want int) {
	b.Helper()
	got, bad := 0, false
	var prev []byte
	err := s.scan([]byte("key-"), []byte("key-~"), func(k, _ []byte) bool {
		if got > 0 && bytes.Compare(prev, k) >= 0 {
			bad = true
			return false
		}
		prev = append(prev[:0], k...) // v3 reuses its key buffer
		got++
		return true
	})
	if err != nil || bad || got != want {
		b.Fatalf("CORRECTNESS: scan gave %d keys (want %d), order broken=%v, err=%v", got, want, bad, err)
	}
}

// ---- benchmarks: b.N counts operations, so ns/op and allocs/op are per key ----

// benchPut fills a fresh tree to n keys, rebuilds it (timer stopped), and
// repeats. The last fill may be partial; verify checks what it holds.
func benchPut(b *testing.B, n int, open func() (store, error), random bool) {
	keys := makeKeys(n)
	if random {
		keys = shuffled(keys)
	}
	var s store
	b.ReportAllocs()
	g := startGC()
	b.ResetTimer()
	for i := range b.N {
		j := i % n
		if j == 0 {
			b.StopTimer()
			if s != nil {
				mustClose(b, s)
			}
			s = mustOpen(b, open)
			b.StartTimer()
		}
		if err := s.put(keys[j], keys[j]); err != nil {
			b.Fatalf("put %s: %v", keys[j], err)
		}
	}
	b.StopTimer()
	g.report(b)
	done := (b.N-1)%n + 1
	verify(b, s, keys[:done], keys[:done])
	verifyScan(b, s, done)
	mustClose(b, s)
}

func BenchmarkPutSequential(b *testing.B) {
	each(b, func(b *testing.B, n int, open func() (store, error)) { benchPut(b, n, open, false) })
}

func BenchmarkPutRandom(b *testing.B) {
	each(b, func(b *testing.B, n int, open func() (store, error)) { benchPut(b, n, open, true) })
}

// BenchmarkPutUpdateHeavy: n writes over n/10 keys, so 90% are overwrites.
func BenchmarkPutUpdateHeavy(b *testing.B) {
	each(b, func(b *testing.B, n int, open func() (store, error)) {
		keyspace := max(1, n/10)
		keys := makeKeys(keyspace)
		vals := make([][]byte, n)
		for j := range vals {
			vals[j] = fmt.Appendf(nil, "v%d", j)
		}
		var s store
		b.ReportAllocs()
		g := startGC()
		b.ResetTimer()
		for i := range b.N {
			j := i % n
			if j == 0 {
				b.StopTimer()
				if s != nil {
					mustClose(b, s)
				}
				s = mustOpen(b, open)
				b.StartTimer()
			}
			if err := s.put(keys[j%keyspace], vals[j]); err != nil {
				b.Fatalf("put: %v", err)
			}
		}
		b.StopTimer()
		g.report(b)
		done := (b.N-1)%n + 1
		want := make([][]byte, keyspace)
		for j := range done {
			want[j%keyspace] = vals[j] // last write wins
		}
		live := min(done, keyspace)
		verify(b, s, keys[:live], want[:live])
		verifyScan(b, s, live)
		mustClose(b, s)
	})
}

// benchGet builds the tree in random order (a realistic fill for both
// engines), then probes in sequential or random order.
func benchGet(b *testing.B, n int, open func() (store, error), random bool) {
	keys := makeKeys(n)
	s := mustOpen(b, open)
	for _, k := range shuffled(keys) {
		if err := s.put(k, k); err != nil {
			b.Fatal(err)
		}
	}
	probe := keys
	if random {
		probe = shuffled(keys)
	}
	b.ReportAllocs()
	g := startGC()
	b.ResetTimer()
	for i := range b.N {
		k := probe[i%n]
		v, ok, err := s.get(k)
		if err != nil || !ok || !bytes.Equal(v, k) {
			b.Fatalf("CORRECTNESS: get %s: ok=%v err=%v", k, ok, err)
		}
	}
	b.StopTimer()
	g.report(b)
	mustClose(b, s)
}

func BenchmarkGetSequential(b *testing.B) {
	each(b, func(b *testing.B, n int, open func() (store, error)) { benchGet(b, n, open, false) })
}

func BenchmarkGetRandom(b *testing.B) {
	each(b, func(b *testing.B, n int, open func() (store, error)) { benchGet(b, n, open, true) })
}

// BenchmarkDeleteRandom deletes every key in random order, then rebuilds
// (timer stopped). v2 merges half-empty pages; v3 doesn't merge yet.
func BenchmarkDeleteRandom(b *testing.B) {
	each(b, func(b *testing.B, n int, open func() (store, error)) {
		order := shuffled(makeKeys(n))
		var s store
		b.ReportAllocs()
		g := startGC()
		b.ResetTimer()
		for i := range b.N {
			j := i % n
			if j == 0 {
				b.StopTimer()
				if s != nil {
					mustClose(b, s)
				}
				s = mustOpen(b, open)
				for _, k := range order {
					if err := s.put(k, k); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
			}
			if ok, err := s.del(order[j]); err != nil || !ok {
				b.Fatalf("CORRECTNESS: delete %s: ok=%v err=%v", order[j], ok, err)
			}
		}
		b.StopTimer()
		g.report(b)
		done := (b.N-1)%n + 1
		verifyGone(b, s, order[:done])
		verify(b, s, order[done:], order[done:])
		verifyScan(b, s, n-done)
		mustClose(b, s)
	})
}

// BenchmarkScan100 reads 100 consecutive keys from a pseudo-random start.
func BenchmarkScan100(b *testing.B) {
	each(b, func(b *testing.B, n int, open func() (store, error)) {
		keys := makeKeys(n + 1) // keys[n] is only ever an end bound
		s := mustOpen(b, open)
		for _, k := range shuffled(keys[:n]) {
			if err := s.put(k, k); err != nil {
				b.Fatal(err)
			}
		}
		size := min(100, n)
		span := uint64(n - size + 1)
		got := 0
		count := func(_, _ []byte) bool { got++; return true } // made once: no per-op closure alloc
		b.ReportAllocs()
		g := startGC()
		b.ResetTimer()
		for i := range b.N {
			start := int(uint64(i) * 2654435761 % span)
			got = 0
			if err := s.scan(keys[start], keys[start+size], count); err != nil || got != size {
				b.Fatalf("CORRECTNESS: scan from %s: %d keys, want %d (err %v)", keys[start], got, size, err)
			}
		}
		b.StopTimer()
		g.report(b)
		mustClose(b, s)
	})
}

// BenchmarkGCCost times one full forced GC cycle with the tree loaded.
// This is the cost v3 exists to remove: v2's pages and nodes are heap
// objects the collector must find and mark; v3's pages sit in an mmap'd
// arena the collector never sees.
func BenchmarkGCCost(b *testing.B) {
	each(b, func(b *testing.B, n int, open func() (store, error)) {
		s := mustOpen(b, open)
		for _, k := range shuffled(makeKeys(n)) {
			if err := s.put(k, k); err != nil {
				b.Fatal(err)
			}
		}
		// The key slices are now garbage: what stays on the heap is the tree.
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		b.ResetTimer() // ResetTimer also deletes reported metrics, so report after
		for range b.N {
			runtime.GC()
		}
		b.StopTimer()
		b.ReportMetric(float64(m.HeapAlloc)/(1<<20), "heap-MiB")
		b.ReportMetric(float64(m.HeapObjects), "heap-objs")
		runtime.KeepAlive(s)
		mustClose(b, s)
	})
}
