package page

import (
	"encoding/binary"
	"testing"
)

// FuzzTornSlot feeds arbitrary 8-byte words into the slot directory (and,
// optionally, into slotCount and heapStart) and runs the reader path over the
// result. Under lock-free reads a reader can see exactly this kind of torn
// state. The reader must return ok=false / an error or a value, and must never
// panic or read out of bounds (P23). A panic fails the fuzz run by itself, so
// the test needs no assertions.
//
// Input layout: [flags][slotCount u16][heapStart u16][slot words, 8 bytes each...]
func FuzzTornSlot(f *testing.F) {
	f.Add(make([]byte, 5+8*4))                                                           // all-zero slots
	f.Add(append([]byte{0, 0, 0, 0, 0}, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)) // all-ones slot
	f.Add([]byte("70\x160\xcb"))                                                         // P56: heapStart 52,016 > PageSize let the search index past the page
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 5 {
			return
		}
		flags, count, heap := data[0], binary.LittleEndian.Uint16(data[1:]), binary.LittleEndian.Uint16(data[3:])
		data = data[5:]

		// 1. A real page: three inline keys and one overflow key.
		src := newMemPages()
		env := &Env{Scratch: newTestPage(), Pages: src}
		p := newLeaf(t)
		for _, k := range []string{"a", "b", "c"} {
			wantErr(t, "Insert", p.Insert([]byte(k), patterned(100, k[0]), env), nil)
		}
		wantErr(t, "Insert", p.Insert([]byte("d"), patterned(20_000, 'd'), env), nil)

		// 2. Tear it: overwrite slots with words from the input.
		n := int(p.SlotCount())
		for i := 0; i < n && len(data) >= 8; i++ {
			p.setSlotAt(i, slot(binary.LittleEndian.Uint64(data)))
			data = data[8:]
		}

		// 3. Optionally tear the header too (a count or heapStart from another moment).
		if flags&1 != 0 {
			p.SetSlotCount(count)
		}
		if flags&2 != 0 {
			p.SetHeapStart(heap)
		}

		// 4. Run the reader path for every key, present or not.
		for k := byte('a'); k <= 'h'; k++ {
			key := []byte{k}
			p.SeekSlot(key)
			p.Get(key, nil, env)
		}
	})
}
