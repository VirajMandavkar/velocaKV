package page

import "testing"

func TestGeometry(t *testing.T) {
	cases := []struct {
		name      string
		got, want int
	}{
		{"PageBits", PageBits, wantPageBits},
		{"PageSize", PageSize, wantPageSize},
		{"HeaderSize", HeaderSize, 64},
		{"SlotSize", SlotSize, 8},
		{"Usable", Usable, wantUsable},
		{"MaxKeyLen", MaxKeyLen, 512},
		{"MaxFenceBytes", MaxFenceBytes, 1024},
		{"MaxInlineRecord", MaxInlineRecord, wantMaxInlineRecord},
		{"SlabBytes", SlabBytes, 67108864},
		{"PagesPerSlab", PagesPerSlab, wantPagesPerSlab},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
