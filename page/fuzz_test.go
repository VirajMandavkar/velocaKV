package page

import (
	"bytes"
	"errors"
	"testing"
)

func FuzzPage(f *testing.F) {
	// addBoth adds a seed twice: once with no fences (mode 0), once fenced (mode 1).
	addBoth := func(ops []byte) {
		f.Add(append([]byte{0}, ops...))
		f.Add(append([]byte{1}, ops...))
	}
	addBoth(nil) // empty script

	// 1. Fill until the page is full: medium records (490 B) under 24 distinct keys.
	//    8 KiB holds 16 of them, 4 KiB holds 8; the next Insert returns ErrNeedSplit.
	var fill []byte
	for kb := byte(4); kb < 28; kb++ {
		fill = append(fill, 0, kb, 239) // Insert fuzzKey(kb), 490 B
	}
	addBoth(fill)

	// 2. Overflow life cycle on one key.
	addBoth([]byte{
		0, 0, 255, // Insert "kaa", MaxInlineRecord+1,500 B → overflow
		1, 0, 2, // Update "kaa", 2 B                       → overflow to inline
		1, 0, 250, // Update "kaa", MaxInlineRecord+1,000 B → inline to overflow
		3, 0, 0, // Get "kaa"
		2, 0, 0, // Delete "kaa"                            → chain freed
	})

	// 3. Churn, then compact, then read everything.
	addBoth([]byte{
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

	// 4. Long script: fill the page with every key (31 B values, 150+ slots,
	//    inserts land mid-directory), delete every 3rd key (dead bytes), insert
	//    those keys again (forces compact-before-insert), then read everything.
	var long []byte
	for kb := range 256 {
		long = append(long, 0, byte(kb), 31) // Insert fuzzKey(kb), 31 B
	}
	for kb := 0; kb < 256; kb += 3 {
		long = append(long, 2, byte(kb), 0) // Delete fuzzKey(kb)
	}
	for kb := 0; kb < 256; kb += 3 {
		long = append(long, 0, byte(kb), 31) // Insert it again
	}
	for kb := range 256 {
		long = append(long, 3, byte(kb), 0) // Get fuzzKey(kb)
	}
	addBoth(long)

	// 5. Put inserts and replaces records, including overflow transitions,
	//    then runs out of leaf space on a sequence of distinct keys.
	var put []byte
	for _, op := range [][3]byte{
		{5, 0, 4},   // insert "kaa"
		{5, 0, 8},   // replace "kaa"
		{5, 1, 255}, // insert "kab" with overflow value
		{5, 1, 2},   // replace overflow value with inline value
		{5, 1, 255}, // replace inline value with overflow value
		{2, 1, 0},   // delete "kab"
		{5, 1, 4},   // reinsert "kab"
	} {
		put = append(put, op[:]...)
	}
	for kb := byte(2); kb < 26; kb++ {
		put = append(put, 5, kb, 239) // Put distinct keys with 490 B values
	}
	addBoth(put)

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		mode := data[0]
		data = data[1:]
		p := newTestPage()
		low, high := fuzzFences(mode)
		if err := p.Format(KindLeaf, low, high); err != nil {
			t.Fatalf("Format: %v", err)
		}
		p1 := int(p.PrefixLen())
		src := newMemPages()
		env := &Env{Scratch: newTestPage(), Pages: src}
		model := map[string][]byte{}
		maxSlots := 0

		for len(data) >= 3 {
			op, kb, sb := data[0], data[1], data[2]
			data = data[3:]
			key := fuzzKey(kb)
			val := patterned(fuzzValLen(sb), kb)
			suffix := key[p1:]

			_, exists := model[string(key)]

			switch op % 6 {
			case 0: // Insert
				err := p.Insert(suffix, val, env)
				switch {
				case exists && errors.Is(err, ErrKeyExists):
				case !exists && err == nil:
					model[string(key)] = val
				case !exists && errors.Is(err, ErrNeedSplit):
				default:
					t.Fatalf("Insert (%q, %d bytes): err = %v, key in model = %t", key, len(val), err, exists)
				}
			case 1: // Update
				err := p.Update(suffix, val, env)
				switch {
				case !exists && errors.Is(err, ErrKeyNotFound):
				case exists && err == nil:
					model[string(key)] = val
				case exists && errors.Is(err, ErrNeedSplit):
				default:
					t.Fatalf("Update (%q, %d bytes): err = %v, key in model = %t", key, len(val), err, exists)
				}
			case 2: // Delete
				err := p.Delete(suffix, env)
				switch {
				case !exists && errors.Is(err, ErrKeyNotFound):
				case exists && err == nil:
					delete(model, string(key))
				default:
					t.Fatalf("Delete (%q): err = %v, key in model = %t", key, err, exists)
				}
			case 3: // Get
				got, err := p.Get(suffix, nil, env)
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
			case 5:
				inserted, err := p.Put(suffix, val, env)
				switch {
				case err == nil:
					if inserted == exists {
						t.Fatalf("Put(%q): inserted = %v, but key in model = %v", key, inserted, exists)
					}
					model[string(key)] = val
				case errors.Is(err, ErrNeedSplit):
				default:
					t.Fatalf("Put(%q, %d bytes): err = %v", key, len(val), err)
				}
			}
			mustCheck(t, p)
			maxSlots = max(maxSlots, int(p.SlotCount()))
		}
		t.Logf("max slots: %d", maxSlots) // TEMP
		if int(p.SlotCount()) != len(model) {
			t.Fatalf("Key count got %d want %d", int(p.SlotCount()), len(model))
		}

		for k, v := range model {
			got, err := p.Get([]byte(k)[p1:], nil, env)
			if !bytes.Equal(got, v) || err != nil {
				t.Fatalf("Get(%q) : err : %v, key, val in model (%q,%q), got %q.", k, err, k, v, got)
			}
		}
		want := 0
		for k, v := range model {
			if len(k)+len(v)-p1 > MaxInlineRecord {
				want += overflowPages(len(v))
			}
		}
		if got := src.live(); got != want {
			t.Fatalf("live overflow pages = %d, want %d (leak or double free)", got, want)
		}
	})
}

func fuzzKey(kb byte) []byte {
	stems := [4]string{"kaa", "kab", "kba", "kbb"}
	stem := stems[kb&3]
	tailLen := int(kb>>2) & 3
	tailByte := (kb >> 4) * 0x11

	key := []byte(stem)
	for range tailLen {
		key = append(key, tailByte)
	}
	return key
}

func TestFuzzKey(t *testing.T) {
	tests := []struct {
		kb   byte
		want string
	}{
		{0x00, "kaa"},
		{0x05, "kab\x00"},
		{0x2E, "kba\x22\x22\x22"},
		{0xFF, "kbb\xff\xff\xff"},
	}

	for _, tt := range tests {
		if got := fuzzKey(tt.kb); !bytes.Equal(got, []byte(tt.want)) {
			t.Errorf("fuzzKey(%#x) = %q, want %q", tt.kb, got, tt.want)
		}
	}

	seen := map[string]bool{}
	for kb := range 256 {
		seen[string(fuzzKey(byte(kb)))] = true
	}
	if len(seen) != 196 {
		t.Errorf("distinct keys = %d, want 196", len(seen))
	}
}

func fuzzValLen(sb byte) int {
	switch {
	case sb < 200:
		return int(sb) % 32
	case sb < 240:
		return 100 + int(sb-200)*10
	default:
		return MaxInlineRecord + int(sb-240)*100
	}
}

func TestFuzzValLen(t *testing.T) {
	tests := []struct {
		sb   byte
		want int
	}{
		{0, 0}, {31, 31}, {32, 0}, {199, 7}, {200, 100}, {239, 490}, {240, MaxInlineRecord}, {255, MaxInlineRecord + 1500},
	}
	for _, tt := range tests {
		if got := fuzzValLen(tt.sb); got != tt.want {
			t.Errorf("fuzzValLen(%d) = %d, want %d", tt.sb, got, tt.want)
		}
	}
}

// fuzzFences picks the page's fences: none (even mode) or ["ka", "kc") (odd mode).
func fuzzFences(mode byte) (low, high []byte) {
	if mode%2 == 0 {
		return nil, nil
	}
	return []byte("ka"), []byte("kc")
}
