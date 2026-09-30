//go:build page4k

package page

// Hand-computed expected values for the page4k build.
const (
	wantPageBits        = 12
	wantPageSize        = 4096
	wantUsable          = 4032
	wantMaxInlineRecord = 736
	wantPagesPerSlab    = 16384
)
