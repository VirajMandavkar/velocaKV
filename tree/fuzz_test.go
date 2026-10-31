package tree

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"velocakv/arena"
	"velocakv/page"
)

const (
	poolFamily  = 1024
	poolSize    = 4 * poolFamily
	fuzzWorkers = 3
	maxFuzzOps  = 300
)

var (
	poolOnce sync.Once
	pool     [poolSize][]byte
)

// fuzzPool builds the key pool once: four families of 1024 keys in disjoint
// key ranges ("s" < "u" < "x" < "z"):
//
//	short   "s0000"                 5 B: many per page
//	prefix  "user:0000:profile"    17 B: a shared prefix, so pages get prefix >= 1
//	long    300×"x" + "0000"      304 B
//	max     508×"z" + "0000"      512 B = MaxKeyLen: ~15 per page, deep trees fast
func fuzzPool() {
	poolOnce.Do(func() {
		x, z := strings.Repeat("x", 300), strings.Repeat("z", page.MaxKeyLen-4)
		for i := range poolFamily {
			pool[i] = fmt.Appendf(nil, "s%04d", i)
			pool[poolFamily+i] = fmt.Appendf(nil, "user:%04d:profile", i)
			pool[2*poolFamily+i] = fmt.Appendf(nil, "%s%04d", x, i)
			pool[3*poolFamily+i] = fmt.Appendf(nil, "%s%04d", z, i)
		}
	})
}

// fuzzValue picks a value size from sb and fills it from (key, version), so
// a value from the wrong key or an older write can never match the model.
//
//	sb < 160      0–40 B             small
//	160 <= sb < 220  inline limit ± 30   crosses the inline/overflow boundary for this key
//	sb >= 220     2,000–19,500 B     overflow chains
func fuzzValue(keyIdx, ver int, sb byte, keyLen int) []byte {
	var n int
	switch {
	case sb < 160:
		n = int(sb) % 41
	case sb < 220:
		n = max(0, page.MaxInlineRecord-keyLen+int(sb)-190)
	default:
		n = 2000 + int(sb-220)*500
	}
	v := make([]byte, n)
	for j := range v {
		v[j] = byte(keyIdx*31 + ver*7 + j)
	}
	return v
}

type fuzzInput struct{ b []byte }

func (in *fuzzInput) byte1() (byte, bool) {
	if len(in.b) == 0 {
		return 0, false
	}
	c := in.b[0]
	in.b = in.b[1:]
	return c, true
}

func (in *fuzzInput) index() (int, bool) {
	hi, ok1 := in.byte1()
	lo, ok2 := in.byte1()
	return (int(hi)<<8 | int(lo)) % poolSize, ok1 && ok2
}

// modelRange returns the model's keys in [start, end), sorted.
// Go compares strings byte by byte, exactly like bytes.Compare.
func modelRange(m map[string][]byte, start, end []byte) []string {
	var ks []string
	for k := range m {
		if (start == nil || k >= string(start)) && (end == nil || k < string(end)) {
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	return ks
}

func FuzzTree(f *testing.F) {
	// ---- seeds: hand-written starting points the fuzzer mutates ----
	op := func(code, worker int, rest ...byte) []byte {
		return append([]byte{byte(code | worker<<3)}, rest...)
	}
	ix := func(i int) []byte { return []byte{byte(i >> 8), byte(i)} }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

	f.Add(cat( // put, get, delete, get: one key, three workers
		op(0, 0, append(ix(5), 10)...), op(4, 1, ix(5)...),
		op(3, 2, ix(5)...), op(4, 0, ix(5)...)))
	f.Add(cat( // ascending bulk loads of 512-byte keys: deep tree, append hint
		op(6, 0, append(ix(3*poolFamily), 60)...),
		op(6, 1, append(ix(3*poolFamily+60), 60)...),
		op(6, 2, append(ix(3*poolFamily+120), 60)...),
		op(6, 0, append(ix(3*poolFamily+180), 60)...),
		op(5, 1, append(append(ix(0), ix(0)...), 3)...))) // full scan
	f.Add(cat( // descending bulk loads: prepend hint
		op(6, 0, append(ix(2*poolFamily+500), 0x80|60)...),
		op(6, 1, append(ix(2*poolFamily+440), 0x80|60)...),
		op(6, 2, append(ix(2*poolFamily+380), 0x80|60)...)))
	f.Add(cat( // overflow puts, then shrink them, then delete
		op(0, 0, append(ix(poolFamily+1), 250)...), op(0, 1, append(ix(poolFamily+2), 230)...),
		op(0, 2, append(ix(poolFamily+1), 5)...), op(3, 0, ix(poolFamily+2)...),
		op(5, 1, append(append(ix(poolFamily), ix(2*poolFamily)...), 0)...)))
	f.Add(cat( // values around the inline limit for 512-byte keys
		op(0, 0, append(ix(3*poolFamily+7), 185)...), op(0, 1, append(ix(3*poolFamily+7), 195)...),
		op(0, 2, append(ix(3*poolFamily+7), 189)...), op(4, 0, ix(3*poolFamily+7)...)))

	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzPool()
		tr := newTestTree(t)
		var ws [fuzzWorkers]*Worker
		for i := range ws {
			w, err := tr.NewWorker()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(w.Close)
			ws[i] = w
		}
		model := map[string][]byte{}
		ver := 0
		in := &fuzzInput{data}

		put := func(w *Worker, i int, v []byte) bool {
			if err := w.Put(pool[i], v); err != nil {
				if errors.Is(err, arena.ErrArenaFull) {
					return false // a full arena is a legal outcome: stop this input
				}
				t.Fatalf("Put(%.20q…): %v", pool[i], err)
			}
			model[string(pool[i])] = v
			return true
		}

	ops:
		for n := 0; n < maxFuzzOps; n++ {
			b0, ok := in.byte1()
			if !ok {
				break
			}
			w := ws[int(b0>>3)%fuzzWorkers]

			switch b0 % 8 {
			case 0, 1, 2, 7: // Put
				i, ok1 := in.index()
				sb, ok2 := in.byte1()
				if !ok1 || !ok2 {
					break ops
				}
				ver++
				if !put(w, i, fuzzValue(i, ver, sb, len(pool[i]))) {
					break ops
				}

			case 3: // Delete
				i, ok := in.index()
				if !ok {
					break ops
				}
				k := string(pool[i])
				err := w.Delete(pool[i])
				if _, in := model[k]; in {
					if err != nil {
						t.Fatalf("op %d: Delete(%.20q…) of a stored key: %v", n, k, err)
					}
					delete(model, k)
				} else if !errors.Is(err, ErrNotFound) {
					t.Fatalf("op %d: Delete(%.20q…) of a missing key: %v, want ErrNotFound", n, k, err)
				}

			case 4: // Get
				i, ok := in.index()
				if !ok {
					break ops
				}
				got, err := w.Get(pool[i], nil)
				if want, in := model[string(pool[i])]; in {
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("op %d: Get(%.20q…): %d bytes, %v; want %d bytes", n, pool[i], len(got), err, len(want))
					}
				} else if !errors.Is(err, ErrNotFound) {
					t.Fatalf("op %d: Get(%.20q…) of a missing key: %v, want ErrNotFound", n, pool[i], err)
				}

			case 5: // Scan
				a, ok1 := in.index()
				b, ok2 := in.index()
				fl, ok3 := in.byte1()
				if !ok1 || !ok2 || !ok3 {
					break ops
				}
				var start, end []byte
				if fl&1 == 0 {
					start = pool[a]
				}
				if fl&2 == 0 {
					end = pool[b]
				}
				want := modelRange(model, start, end)
				got := 0
				err := w.Scan(start, end, func(k, v []byte) bool {
					if got >= len(want) || string(k) != want[got] || !bytes.Equal(v, model[want[got]]) {
						t.Errorf("op %d: Scan gave %.20q… at position %d", n, k, got)
						return false
					}
					got++
					return true
				})
				if err != nil || t.Failed() || got != len(want) {
					t.Fatalf("op %d: Scan(%.20q…, %.20q…): %d keys, want %d (err %v)", n, start, end, got, len(want), err)
				}

			case 6: // Bulk put: a run of consecutive keys, ascending or descending
				i, ok1 := in.index()
				c, ok2 := in.byte1()
				if !ok1 || !ok2 {
					break ops
				}
				count := 20 + int(c&0x7F)%41
				count = min(count, (i/poolFamily+1)*poolFamily-i) // stay inside one family
				for j := range count {
					ki := i + j
					if c&0x80 != 0 {
						ki = i + count - 1 - j
					}
					ver++
					if !put(w, ki, fuzzValue(ki, ver, 16, 0)) {
						break ops
					}
				}
			}

			if err := checkTree(w, fuzzWorkers); err != nil {
				t.Fatalf("op %d: checkTree: %v", n, err)
			}
		}

		// Final: every key the model holds reads back, and a full scan matches.
		for k, want := range model {
			got, err := ws[0].Get([]byte(k), nil)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("final Get(%.20q…): %d bytes, %v", k, len(got), err)
			}
		}
		all := modelRange(model, nil, nil)
		got := 0
		if err := ws[1].Scan(nil, nil, func(k, v []byte) bool {
			ok := got < len(all) && string(k) == all[got]
			got++
			return ok
		}); err != nil || got != len(all) {
			t.Fatalf("final Scan: %d keys, want %d (err %v)", got, len(all), err)
		}
	})
}
