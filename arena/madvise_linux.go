package arena

import "syscall"

// adviseHuge asks the kernel to back b with transparent huge pages
// (2 MiB instead of 4 KiB). It is a hint: errors are ignored and
// correctness never depends on it.
func adviseHuge(b []byte) {
	_ = syscall.Madvise(b, syscall.MADV_HUGEPAGE)
}

const madvPopulateWrite = 23 // MADV_POPULATE_WRITE (Linux 5.14+); package syscall has no name for it

// populate asks the kernel to give b real RAM now, without writing any data.
// Errors (an older kernel) are ignored: the pages just fault in on first use.
func populate(b []byte) { _ = syscall.Madvise(b, madvPopulateWrite) }
