//go:build !page4k

package page

// Hand-computed expected values for the default 8 KiB build.
const (
	wantPageBits        = 13
	wantPageSize        = 8192
	wantUsable          = 8128
	wantMaxInlineRecord = 1760
	wantPagesPerSlab    = 8192
)
