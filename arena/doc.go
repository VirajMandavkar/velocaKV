// Package arena provides VelocaKV's page memory: fixed sized pages carved out of 64MiB slabs
// obtained with anonymous mmap , so the memory lives outside the Go heap and is never scanned or counter by the grabage collector
//
// Every pagae have a unique 32-but page ID. ID 0 is never a real page - its just mean "none" (the end of free list or overflow chain)
// The arena owns each page's gen field and increment it everytime the page is handed out by Alloc,
// so a stale refrence to a receycled page can be detected.

package arena
