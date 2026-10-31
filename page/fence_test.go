package page

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unsafe"
)

// Test 1: Format stores fences at the top of the heap and derives the prefix.
func TestFormat(t *testing.T) {
	cases := []struct {
		name       string
		low, high  string
		wantPrefix string
	}{
		{"whole key space", "", "", ""},
		{"no lower bound", "", "m", ""},
		{"no upper bound", "user:alice", "", ""},
		{"shared prefix", "user:alice", "user:bob", "user:"},
		{"differ at last byte", "abc", "abd", "ab"},
		{"low is prefix of high", "ab", "abc", "ab"},
		{"nothing shared", "apple", "zebra", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newTestPage()
			if err := p.Format(KindLeaf, []byte(c.low), []byte(c.high)); err != nil {
				t.Fatal(err)
			}
			if err := p.Check(); err != nil {
				t.Fatal(err)
			}
			if got := string(p.LowFence()); got != c.low {
				t.Errorf("LowFence = %q, want %q", got, c.low)
			}
			high, inf := p.HighFence()
			if inf != (c.high == "") || string(high) != c.high {
				t.Errorf("HighFence = %q inf=%v, want %q", high, inf, c.high)
			}
			if got := string(p.Prefix()); got != c.wantPrefix {
				t.Errorf("Prefix = %q, want %q", got, c.wantPrefix)
			}
			// Layout: high fence ends at PageSize, low fence sits right below it.
			wantHeap := PageSize - len(c.low) - len(c.high)
			if int(p.HeapStart()) != wantHeap || int(p.LowFenceOff()) != wantHeap {
				t.Errorf("heapStart=%d lowFenceOff=%d, want both %d",
					p.HeapStart(), p.LowFenceOff(), wantHeap)
			}
			if p.fenceBytes() != len(c.low)+len(c.high) {
				t.Errorf("fenceBytes = %d, want %d", p.fenceBytes(), len(c.low)+len(c.high))
			}
		})
	}
}

// Test 2: Format refuses bad fences and leaves the page untouched.
func TestFormatRejects(t *testing.T) {
	long := strings.Repeat("k", MaxKeyLen+1)
	cases := []struct {
		name      string
		kind      uint8
		low, high string
	}{
		{"low > high", KindLeaf, "b", "a"},
		{"low = high", KindLeaf, "a", "a"},
		{"low too long", KindLeaf, long, ""},
		{"high too long", KindLeaf, "", long},
		{"overflow kind", KindOverflow, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newTestPage()
			p.SetGen(7) // marker: must survive a rejected Format
			err := p.Format(c.kind, []byte(c.low), []byte(c.high))
			if !errors.Is(err, ErrPageInvariant) {
				t.Fatalf("want ErrPageInvariant, got %v", err)
			}
			if p.Gen() != 7 {
				t.Errorf("rejected Format modified the page")
			}
		})
	}
}

// Test 3: the longest legal fences (512 + 512 bytes) fit and check clean.
func TestFormatMaxFences(t *testing.T) {
	low := bytes.Repeat([]byte{'a'}, MaxKeyLen)
	high := bytes.Repeat([]byte{'b'}, MaxKeyLen)
	p := newTestPage()
	if err := p.Format(KindLeaf, low, high); err != nil {
		t.Fatal(err)
	}
	if err := p.Check(); err != nil {
		t.Fatal(err)
	}
	if want := PageSize - MaxFenceBytes; int(p.HeapStart()) != want {
		t.Errorf("heapStart = %d, want %d", p.HeapStart(), want)
	}
}

// Test 4: Check catches each kind of corruption. Start from a good page,
// break exactly one thing, expect ErrPageInvariant.
func TestCheckCatches(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(p *Page)
	}{
		{"unknown kind", func(p *Page) { p.SetKind(3) }},
		{"unknown flag bit", func(p *Page) { p.SetFlags(1 << 3) }},
		{"invalid insert hint", func(p *Page) { p.SetFlags(3 << hintShift) }},
		{"slots run into heap", func(p *Page) { p.SetSlotCount(uint16((int(p.HeapStart())-HeaderSize)/SlotSize + 1)) }},
		{"heapStart past page end", func(p *Page) { p.SetHeapStart(PageSize + 1) }},
		{"low fence below heapStart", func(p *Page) { p.SetLowFenceOff(p.HeapStart() - 1) }},
		{"high fence past page end", func(p *Page) { p.SetHighFenceOff(PageSize - 1) }},
		{"fence too long", func(p *Page) { p.SetLowFenceLen(MaxKeyLen + 1) }},
		{"fences overlap", func(p *Page) { p.SetLowFenceOff(p.HighFenceOff()) }},
		{"low >= high", func(p *Page) { p[p.LowFenceOff()+5] = 'z' }}, // "user:alice" -> "user:zlice"
		{"prefixLen wrong", func(p *Page) { p.SetPrefixLen(p.PrefixLen() + 1) }},
		{"accounting drift", func(p *Page) { p.SetDeadBytes(1) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newTestPage()
			if err := p.Format(KindLeaf, []byte("user:alice"), []byte("user:bob")); err != nil {
				t.Fatal(err)
			}
			c.corrupt(p)
			if err := p.Check(); !errors.Is(err, ErrPageInvariant) {
				t.Fatalf("Check missed it: got %v", err)
			}
		})
	}
}

// Test 5: Check rejects a misaligned page (1 byte off).
func TestCheckMisaligned(t *testing.T) {
	buf := new([PageSize/8 + 1]uint64)
	p := (*Page)(unsafe.Add(unsafe.Pointer(buf), 1))
	if err := p.Check(); !errors.Is(err, ErrPageInvariant) {
		t.Fatalf("want ErrPageInvariant for misaligned page, got %v", err)
	}
}

// Test 6: fence and prefix reads do not allocate.
func TestFenceReadsNoAlloc(t *testing.T) {
	p := newTestPage()
	if err := p.Format(KindLeaf, []byte("user:alice"), []byte("user:bob")); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		_ = p.LowFence()
		_, _ = p.HighFence()
		_ = p.Prefix()
	})
	if allocs != 0 {
		t.Errorf("fence reads allocated %.0f times", allocs)
	}
}

func TestCheckFenceRange(t *testing.T) {
	tests := []struct {
		name      string
		low, high string
		suffix    string // what the page stores (key minus prefix)
		wantErr   bool
	}{
		{"inside", "m", "n", "m5", false},
		{"equal to low is inside", "m", "n", "m", false},
		{"below low", "m", "n", "a", true},
		{"equal to high is outside", "m", "n", "n", true},
		{"above high", "m", "n", "z", true},
		{"prefix k: inside", "ka", "kc", "bz", false}, // key "kbz"
		{"prefix k: at high", "ka", "kc", "c", true},  // key "kc"
		{"prefix k: below low", "ka", "kc", "", true}, // key "k" < "ka"
		{"no high fence", "m", "", "zzz", false},      // +inf
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestPage()
			var high []byte
			if tt.high != "" {
				high = []byte(tt.high)
			}
			if err := p.Format(KindLeaf, []byte(tt.low), high); err != nil {
				t.Fatal(err)
			}
			if err := p.Insert([]byte(tt.suffix), []byte("v"), nil); err != nil {
				t.Fatalf("Insert: %v", err)
			}
			err := p.Check()
			if tt.wantErr != (err != nil) || (err != nil && !errors.Is(err, ErrPageInvariant)) {
				t.Errorf("Check() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
