package page

import (
	"bytes"
	"errors"
	"testing"
)

func FuzzPage(f *testing.F) {
	f.Add([]byte{0}) // empty-ish script

	// 1. Fill until the page is full: 5 inserts of near-max records (109*16 = 1,744 B).
	//    On 8 KiB the 5th returns ErrNeedSplit; on 4 KiB they are all overflow records.
	f.Add([]byte{
		0, 0, 109, // Insert "a"
		0, 1, 109, // Insert "b"
		0, 2, 109, // Insert "c"
		0, 3, 109, // Insert "d"
		0, 4, 109, // Insert "e"  → ErrNeedSplit on 8 KiB
	})

	// 2. Overflow life cycle on one key.
	f.Add([]byte{
		0, 0, 200, // Insert "a", 3,200 B → overflow
		1, 0, 2, // Update "a", 32 B   → overflow to inline
		1, 0, 250, // Update "a", 4,000 B → inline to overflow
		3, 0, 0, // Get "a"
		2, 0, 0, // Delete "a"          → chain freed
	})

	// 3. Churn, then compact, then read everything.
	f.Add([]byte{
		0, 0, 4, // Insert "a"
		0, 1, 4, // Insert "b"
		0, 2, 4, // Insert "c"
		0, 3, 4, // Insert "d"
		2, 1, 0, // Delete "b"  → dead bytes
		2, 3, 0, // Delete "d"
		4, 0, 0, // Compact
		3, 0, 0, // Get "a" (present)
		3, 1, 0, // Get "b" (missing → ErrKeyNotFound)
		3, 2, 0, // Get "c"
	})
	f.Fuzz(func(t *testing.T, data []byte) {
		p := newLeaf(t)
		src := newMemPages()
		env := &Env{Scratch: newTestPage(), Pages: src}
		model := map[string][]byte{}

		// const maxOps = 256
		// if len(data) > maxOps*3 {
		// 	data = data[:maxOps*3]
		// }

		for len(data) >= 3 {
			op, kb, sb := data[0], data[1], data[2]
			data = data[3:]
			key := []byte{'a' + kb%8}
			val := patterned(int(sb)*16+int(kb>>3)%16, kb) // every size from 0 to 4,095

			_, exists := model[string(key)]

			switch op % 5 {
			case 0: // Insert
				err := p.Insert(key, val, env)
				switch {
				case exists && errors.Is(err, ErrKeyExists):
				case !exists && err == nil:
					model[string(key)] = val
				case !exists && errors.Is(err, ErrNeedSplit):
				default:
					t.Fatalf("Insert (%q, %d bytes): err = %v, key in model = %t", key, len(val), err, exists)
				}
			case 1: // Update
				err := p.Update(key, val, env)
				switch {
				case !exists && errors.Is(err, ErrKeyNotFound):
				case exists && err == nil:
					model[string(key)] = val
				case exists && errors.Is(err, ErrNeedSplit):
				default:
					t.Fatalf("Update (%q, %d bytes): err = %v, key in model = %t", key, len(val), err, exists)
				}
			case 2: // Delete
				err := p.Delete(key, env)
				switch {
				case !exists && errors.Is(err, ErrKeyNotFound):
				case exists && err == nil:
					delete(model, string(key))
				default:
					t.Fatalf("Delete (%q): err = %v, key in model = %t", key, err, exists)
				}
			case 3: // Get
				got, err := p.Get(key, nil, env)
				switch {
				case exists && err == nil:
					if !bytes.Equal(got, model[string(key)]) {
						t.Fatalf("Get(%q) = %d bytes, want %d bytes (content differs)", key, len(got), len(model[string(key)]))
					}
				case !exists && errors.Is(err, ErrKeyNotFound):
				default:
					t.Fatalf("Get (%q, %d bytes): err = %v, key in model = %t, got : %d", key, len(val), err, exists, len(got))
				}
			case 4: // Compact
				p.Compact(env.Scratch)
			}
			mustCheck(t, p)
		}
		if int(p.SlotCount()) != len(model) {
			t.Fatalf("Key count got %d want %d", int(p.SlotCount()), len(model))
		}

		for k, v := range model {
			got, err := p.Get([]byte(k), nil, env)
			if !bytes.Equal(got, v) || err != nil {
				t.Fatalf("Get(%q) : err : %v, key, val in model (%q,%q), got %q.", k, err, k, v, got)
			}
		}
		want := 0
		for k, v := range model {
			if len(k)+len(v) > MaxInlineRecord {
				want += overflowPages(len(v))
			}
		}
		if got := src.live(); got != want {
			t.Fatalf("live overflow pages = %d, want %d (leak or double free)", got, want)
		}
	})
}
