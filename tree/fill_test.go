package tree

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"velocakv/arena"
	"velocakv/page"
)

// ---------- measurement helpers (test only) ----------

type treeStat struct {
	Height, Leaves, Inners, Keys int
	LeafFill                     float64 // average share of a leaf's usable bytes in use, 0..1
}

// collectStats walks every page from the root and counts levels, pages,
// keys and how full the leaves are.
func collectStats(t *testing.T, w *Worker) treeStat {
	t.Helper()
	var st treeStat
	var fillSum float64
	var visit func(d desc, depth int)
	visit = func(d desc, depth int) {
		p, err := load(w.aw, d)
		if err != nil {
			t.Fatalf("stats walk: %v", err)
		}
		st.Height = max(st.Height, depth+1)
		if p.Kind() == page.KindLeaf {
			n := int(p.SlotCount())
			used := n*page.SlotSize + (page.PageSize - int(p.HeapStart())) - int(p.DeadBytes())
			st.Leaves++
			st.Keys += n
			fillSum += float64(used) / float64(page.Usable)
			return
		}
		st.Inners++
		for i := range int(p.SlotCount()) {
			c, _ := p.ChildAt(i)
			visit(desc(c), depth+1)
		}
		visit(desc(p.UpperChild()), depth+1)
	}
	visit(desc(w.t.root.Load()), 0)
	if st.Leaves > 0 {
		st.LeafFill = fillSum / float64(st.Leaves)
	}
	return st
}

// rssMiB is the process's resident memory (real RAM in use), from Linux's
// /proc/self/status. -1 where that file does not exist.
func rssMiB() float64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return -1
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "VmRSS:"); ok {
			var kb float64
			fmt.Sscan(v, &kb)
			return kb / 1024
		}
	}
	if err := s.Err(); err != nil {
		return -1
	}
	return -1
}

// heapMiB is the live Go heap after a full GC: memory the GC manages.
// Tree pages live in mmap'd slabs, so this should stay flat as the tree grows.
func heapMiB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// ---------- the test ----------

// fillKey returns the i-th key of a fixed shuffle of [0, 2^22): an odd
// multiplier is a bijection modulo a power of two, so no key repeats.
func fillKey(i int) (key []byte, x int) {
	x = int(uint32(i) * 2654435761 % (1 << 22))
	return key11(x), x
}

func TestFillArenaUntilFull(t *testing.T) {
	if testing.Short() {
		t.Skip("fills a 64 MiB arena")
	}
	tr, w := newOpsWorker(t) // exactly one slab

	big := make([]byte, 3000) // overflow on both builds: 1 overflow page each
	val := func(x int) []byte {
		if x%10_000 == 0 {
			return big
		}
		return val16
	}

	t.Logf("%-5s %9s %6s %7s %6s %9s %10s %8s %10s %10s",
		"fill", "keys", "height", "leaves", "inner", "leaf-fill", "live-pages", "RSS-MiB", "GoHeap-MiB", "puts/s")
	last, lastN := time.Now(), 0
	row := func(label string, n int) {
		st, s := collectStats(t, w), tr.a.Stats()
		rate := float64(n-lastN) / time.Since(last).Seconds()
		t.Logf("%-5s %9d %6d %7d %6d %8.1f%% %10d %8.1f %10.1f %10.0f",
			label, st.Keys, st.Height, st.Leaves, st.Inners, st.LeafFill*100, s.Live, rssMiB(), heapMiB(), rate)
		last, lastN = time.Now(), n // after the row: its GC and walk don't count against puts/s
	}

	// 1–2. Fill, logging a row at every 10% of capacity.
	failedAt, nextPct := -1, 10
	var failErr error
	for n := 0; n < 1<<22; n++ {
		k, x := fillKey(n)
		if err := w.Put(k, val(x)); err != nil {
			failedAt, failErr = n, err
			break
		}
		if n%5000 == 0 {
			s := tr.a.Stats()
			for pct := s.Live * 100 / s.Capacity; pct >= nextPct && nextPct < 100; nextPct += 10 {
				row(fmt.Sprintf("%d%%", nextPct), n)
			}
		}
	}
	if failedAt < 0 {
		t.Fatal("the arena never filled")
	}
	row("FULL", failedAt)

	// 3. The failure must be a clean, refused split.
	if !errors.Is(failErr, arena.ErrArenaFull) {
		t.Fatalf("Put #%d failed with %v, want arena.ErrArenaFull", failedAt, failErr)
	}
	s, h := tr.a.Stats(), collectStats(t, w).Height
	left := s.Capacity - s.Live
	t.Logf("full after %d puts: %d pages left, a split here reserves %d (height + 1)", failedAt, left, h+1)
	if left >= h+1 {
		t.Fatalf("Put failed with %d pages left, but a split needs only %d", left, h+1)
	}
	if err := checkTree(w, 1); err != nil { // no half-split, no leaked reserved page
		t.Fatalf("checkTree after ErrArenaFull: %v", err)
	}
	for n := range failedAt { // nothing stored before the failure was lost
		k, x := fillKey(n)
		got, err := w.Get(k, nil)
		if err != nil || len(got) != len(val(x)) {
			t.Fatalf("Get(%q) after full: %d bytes, %v", k, len(got), err)
		}
	}

	// 4. Recovery: free 10 overflow pages, then the refused Put succeeds.
	freed := 0
	for n := 0; n < failedAt && freed < 10; n++ {
		if k, x := fillKey(n); x%10_000 == 0 {
			if err := w.Delete(k); err != nil {
				t.Fatalf("Delete(%q): %v", k, err)
			}
			freed++
		}
	}
	k, x := fillKey(failedAt)
	if err := w.Put(k, val(x)); err != nil {
		t.Fatalf("Put after freeing %d pages: %v", freed, err)
	}
	if err := checkTree(w, 1); err != nil {
		t.Fatalf("checkTree after recovery: %v", err)
	}
	t.Logf("recovered: freed %d overflow pages, the refused Put went through", freed)
}
