package arena

import "velocakv/page"

const (
	windowBytes = 2 << 20                      // one huge page
	windowPages = windowBytes >> page.PageBits // 256 (8 KiB build) or 512 (4 KiB)
)

// startPrefaulter runs one background goroutine that faults in whole
// windows ahead of the fresh-page cursor.
func (a *Arena) startPrefaulter() {
	a.prefaultCh = make(chan uint64, 4)
	a.prefaultStop = make(chan struct{})
	a.prefaultDone = make(chan struct{})
	go func() {
		defer close(a.prefaultDone)
		for {
			select {
			case <-a.prefaultStop:
				return
			case w := <-a.prefaultCh:
				slab, off := split(uint32(w * windowPages))
				s := *a.slabs.Load()
				if slab < len(s) { // window's slab not mapped yet: skip it
					populate(s[slab][off : off+windowBytes])
				}
			}
		}
	}()
}

// hintNext is called with each fresh page ID. On the first page of a
// window it asks for the next window. It never blocks: if the goroutine
// is busy, the hint is dropped and those pages fault in normally.
func (a *Arena) hintNext(n uint64) {
	if n%windowPages != 0 {
		return
	}
	select {
	case a.prefaultCh <- n/windowPages + 1:
	default:
	}
}
