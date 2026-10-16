//go:build !page4k

package page

// PageBits is log2 of the page size. Default build: 8 KiB pages.
const PageBits = 13
