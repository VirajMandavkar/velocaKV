package page

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"
)

// Test 1: the two worked examples from the lesson, bit for bit.
func TestSlotWorkedExamples(t *testing.T) {
	cases := []struct {
		name                string
		off, keyLen, valLen int
		ovf                 bool
		suffix              string
		want                uint64
	}{
		{"apple", 8000, 5, 100, false, "apple", 0xFA00014190617070},
		{"cat overflow stub", 4000, 3, 8, true, "cat", 0x7D0000C022636174},
	}
	for _, c := range cases {
		s := makeSlot(c.off, c.keyLen, c.valLen, c.ovf, keyHead([]byte(c.suffix)))
		if uint64(s) != c.want {
			t.Errorf("%s: got %#016x, want %#016x", c.name, uint64(s), c.want)
		}
		if s.off() != c.off || s.keyLen() != c.keyLen || s.valLen() != c.valLen || s.ovf() != c.ovf {
			t.Errorf("%s: unpack gave off=%d keyLen=%d valLen=%d ovf=%v",
				c.name, s.off(), s.keyLen(), s.valLen(), s.ovf())
		}
	}
}

// Test 2: every field round-trips at 0 and at its max, while all others stay 0.
func TestSlotFieldsIndependent(t *testing.T) {
	maxOff, maxKey, maxVal := offsetMask, keyLenMask, valLenMask
	checks := []struct {
		name string
		s    slot
		want [5]int // off, keyLen, valLen, ovf(0/1), head
	}{
		{"all zero", makeSlot(0, 0, 0, false, 0), [5]int{0, 0, 0, 0, 0}},
		{"off max", makeSlot(maxOff, 0, 0, false, 0), [5]int{maxOff, 0, 0, 0, 0}},
		{"keyLen max", makeSlot(0, maxKey, 0, false, 0), [5]int{0, maxKey, 0, 0, 0}},
		{"valLen max", makeSlot(0, 0, maxVal, false, 0), [5]int{0, 0, maxVal, 0, 0}},
		{"ovf", makeSlot(0, 0, 0, true, 0), [5]int{0, 0, 0, 1, 0}},
		{"head max", makeSlot(0, 0, 0, false, headMask), [5]int{0, 0, 0, 0, headMask}},
	}
	for _, c := range checks {
		ovf := 0
		if c.s.ovf() {
			ovf = 1
		}
		got := [5]int{c.s.off(), c.s.keyLen(), c.s.valLen(), ovf, int(c.s.head())}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
		if uint64(c.s)>>rsvShift&1 != 0 {
			t.Errorf("%s: RSV bit set", c.name)
		}
	}
}

// Test 3: an oversized input is cut to its field and never spills into a neighbour.
func TestSlotNoSpill(t *testing.T) {
	s := makeSlot(0, 0, valLenMask+1, false, 0) // one past the 12-bit max
	if s.keyLen() != 0 || s.ovf() {
		t.Errorf("valLen overflow spilled: keyLen=%d ovf=%v", s.keyLen(), s.ovf())
	}
}

// Test 4: heads from the lesson, including padding.
func TestKeyHead(t *testing.T) {
	cases := []struct {
		suffix string
		want   uint32
	}{
		{"", 0x000000},
		{"a", 0x610000},
		{"ab", 0x616200},
		{"ca", 0x636100},
		{"ca\x00", 0x636100},
		{"cab", 0x636162},
		{"apple", 0x617070},
	}
	for _, c := range cases {
		if got := keyHead([]byte(c.suffix)); got != c.want {
			t.Errorf("keyHead(%q) = %#06x, want %#06x", c.suffix, got, c.want)
		}
	}
}

// Test 5: Rule 4 on random keys. If heads differ, they must order the keys
// exactly like a full byte compare. Equal heads are allowed to mean anything.
func TestKeyHeadOrder(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	key := func() []byte {
		b := make([]byte, r.Intn(6)) // 0..5 bytes: lots of short keys and ties
		for i := range b {
			b[i] = byte(r.Intn(4)) // tiny alphabet {0,1,2,3}: many shared heads
		}
		return b
	}
	for i := 0; i < 200000; i++ {
		a, b := key(), key()
		ha, hb := keyHead(a), keyHead(b)
		full := bytes.Compare(a, b)
		if ha < hb && full >= 0 || ha > hb && full <= 0 {
			t.Fatalf("heads %#06x vs %#06x disagree with keys %q vs %q", ha, hb, a, b)
		}
	}
}

// Test 6: slot i lives at byte 64+8i, little-endian like every page word.
func TestSlotLocation(t *testing.T) {
	p := newTestPage()
	last := (PageSize-HeaderSize)/SlotSize - 1 // last slot that fits in the page
	for _, i := range []int{0, 1, 7, 8, last} {
		s := makeSlot(i, i%keyLenMask, i%valLenMask, i%2 == 0, uint32(i))
		p.setSlotAt(i, s)
		if got := p.slotAt(i); got != s {
			t.Errorf("slot %d: read %#x, wrote %#x", i, uint64(got), uint64(s))
		}
		if raw := binary.LittleEndian.Uint64(p[slotOff(i):]); raw != uint64(s) {
			t.Errorf("slot %d: bytes at %d hold %#x, want %#x", i, slotOff(i), raw, uint64(s))
		}
	}
	if HeaderSize+SlotSize*(last+1) != PageSize {
		t.Errorf("last slot index %d does not end at the page end", last)
	}
}

func TestRecordOK(t *testing.T) {
	tests := []struct {
		name      string
		off       int
		keyLen    int
		valLen    int
		ovf       bool
		heapStart int
		want      bool
	}{
		{"ends exactly at page end", PageSize - 105, 5, 100, false, PageSize - 105, true},
		{"one byte past the end", PageSize - 104, 5, 100, false, PageSize - 105, false},
		{"starts below heapStart", PageSize - 106, 5, 100, false, PageSize - 105, false},
		{"key at max length", PageSize - MaxKeyLen, MaxKeyLen, 0, false, PageSize - MaxKeyLen, true},
		{"key one past max", PageSize - MaxKeyLen - 1, MaxKeyLen + 1, 0, false, PageSize - MaxKeyLen - 1, false},
		{"zero length record", PageSize - 105, 0, 0, false, PageSize - 105, true},
		{"overflow stub, correct size", PageSize - 13, 5, ovfStubSize, true, PageSize - 13, true},
		{"overflow. wrong valLen", PageSize - 105, 5, 100, true, PageSize - 105, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := makeSlot(tt.off, tt.keyLen, tt.valLen, tt.ovf, 0)
			if got := recordOK(s, tt.heapStart); got != tt.want {
				t.Errorf("recordOK() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCmpSlot(t *testing.T) {
	tests := []struct {
		name   string
		search string
		stored string
		want   int
	}{
		{"head decides, less", "apple", "apron", -1},
		{"head decides, greater", "banana", "apron", 1},
		{"tie, heap decides", "apricot", "apron", -1},
		{"exact match", "apron", "apron", 0},
		{"padding tie", "ca", "ca\x00", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestPage()
			off := PageSize - len(tt.stored)
			copy(p[off:], tt.stored)
			s := makeSlot(off, len(tt.stored), 0, false, keyHead([]byte(tt.stored)))
			got := p.cmpSlot(s, []byte(tt.search), keyHead([]byte(tt.search)))
			if got != tt.want {
				t.Fatalf("cmpSlot(%q, %q) = %d, want %d", tt.search, tt.stored, got, tt.want)
			}
		})
	}
}

var sinkSlot slot
var sinkHead uint32

func BenchmarkSlotAtHead(b *testing.B) {
	p := newTestPage()
	p.setSlotAt(5, makeSlot(8000, 5, 100, false, 0x617070))
	for i := 0; i < b.N; i++ {
		sinkHead += p.slotAt(5).head()
	}
}

func BenchmarkKeyHead(b *testing.B) {
	k := []byte("apple")
	for i := 0; i < b.N; i++ {
		sinkHead += keyHead(k)
	}
}

func BenchmarkMakeSlot(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sinkSlot = makeSlot(i&offsetMask, 5, 100, false, 0x617070)
	}
}
