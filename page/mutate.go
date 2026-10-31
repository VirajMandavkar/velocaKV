package page

import (
	"errors"
)

// Errors returned by page operations.
var (
	ErrKeyExists   = errors.New("page: key exists")
	ErrKeyNotFound = errors.New("page: key not found")
	ErrNeedSplit   = errors.New("page: no room") // even after compaction; the tree must split
	ErrTooLarge    = errors.New("page: record too large")
	ErrEmptyKey    = errors.New("page: empty key")
	ErrNoScratch   = errors.New("page: scratch page is nil or the page itself")
	ErrNoPages     = errors.New("page: no page source for overflow")
)

// Env holds the resources a caller lends to page operations. The page never
// owns them; a worker typically keeps one Env and reuses it for every call.
type Env struct {
	// Scratch is the workspace Compact packs records into. It is only
	// needed when an operation has to compact, and must not be the page
	// being modified.
	Scratch *Page

	// Pages supplies and reclaims overflow pages. It is only needed when
	// an operation reads, writes or frees an overflow value.
	Pages PageSource
}

// ---------- space accounting ----------

// freeSpace is the gap between the end of the slot directory and heapStart.
func (p *Page) freeSpace() int {
	return int(p.HeapStart()) - slotOff(int(p.SlotCount()))
}

// liveBytes is the heap space used by live records (suffix + value or stub).
func (p *Page) liveBytes() int {
	total := 0
	for i := range int(p.SlotCount()) {
		s := p.slotAt(i)
		total += s.keyLen() + s.valLen()
	}
	return total
}

// ---------- low-level writes (caller holds the write lock) ----------

// carve copies suffix+val just below heapStart, moves heapStart down, and
// returns the record's offset. The caller has checked that it fits.
func (p *Page) carve(suffix, val []byte) int {
	off := int(p.HeapStart()) - len(suffix) - len(val)
	copy(p[off:], suffix)
	copy(p[off+len(suffix):], val)
	p.SetHeapStart(uint16(off))
	return off
}

// shiftRight moves slots idx..n-1 one position up, opening a gap at idx.
// copy is a memmove, so the overlapping ranges are handled correctly.
func (p *Page) shiftRight(idx, n int) {
	copy(p[slotOff(idx+1):slotOff(n+1)], p[slotOff(idx):slotOff(n)])
}

// shiftLeft closes the gap at idx by moving slots idx+1..n-1 down one
// position, then zeroes the vacated last slot. A zero slot has offset 0,
// which recordOK always rejects, so a reader holding a stale count retries
// instead of reading a duplicate.
func (p *Page) shiftLeft(idx, n int) {
	copy(p[slotOff(idx):slotOff(n-1)], p[slotOff(idx+1):slotOff(n)])
	p.setSlotAt(n-1, 0)
}

// insertAt places a new record at slot position idx. Write order: the record
// bytes first, then the slot, then the count, so a slot never points at
// bytes that are not there yet. The caller has checked that
// len(suffix) + len(val) + SlotSize fits in free space.
func (p *Page) insertAt(idx int, suffix, val []byte, ovf bool) {
	n := p.SlotCount()
	off := p.carve(suffix, val)
	p.shiftRight(idx, int(n))
	p.setSlotAt(idx, makeSlot(off, len(suffix), len(val), ovf, keyHead(suffix)))

	hint := uint8(hintNone)
	switch {
	case n == 0:
	case idx == int(n):
		hint = hintEnd
	case idx == 0:
		hint = hintFront
	}
	p.setCountAndHint(n+1, hint)
}

// replaceAt writes a fresh copy of the record at slot idx and repoints the
// slot to it; the old copy becomes dead bytes. The caller has checked that
// len(suffix) + len(val) fits in free space.
func (p *Page) replaceAt(idx int, suffix, val []byte, ovf bool) {
	old := p.slotAt(idx)
	off := p.carve(suffix, val)
	p.setSlotAt(idx, makeSlot(off, len(suffix), len(val), ovf, keyHead(suffix)))
	p.SetDeadBytes(uint16(int(p.DeadBytes()) + old.keyLen() + old.valLen()))
}

// valueBytes returns the value area of the record s describes: the inline
// value, or the 8-byte stub for an overflow record. It points into the page.
func (p *Page) valueBytes(s slot) []byte {
	start := s.off() + s.keyLen()
	return p[start : start+s.valLen()]
}

// valueOf appends the value described by slot s to dst: the inline bytes, ot
// the whole overflow chain followed through env.Pages. Shared by Get and ValueAt,
// so there is one way to read a value
func (p *Page) valueOf(s slot, dst []byte, env *Env) ([]byte, error) {
	if !s.ovf() {
		return append(dst, p.valueBytes(s)...), nil
	}
	if env == nil || env.Pages == nil {
		return dst, ErrNoPages
	}
	head, total := readStub(p.valueBytes(s))
	out, ok := readChain(env.Pages, head, total, dst)
	if !ok {
		return dst, ErrPageInvariant
	}
	return out, nil
}

// ValueAt appends the value of slot i to dst (inline butes or overflow chain)
// The caller checks i < SlotCount
func (p *Page) ValueAt(i int, dst []byte, env *Env) ([]byte, error) {
	s := p.slotAt(i)
	if !recordOK(s, int(p.HeapStart())) {
		return dst, ErrPageInvariant
	}
	return p.valueOf(s, dst, env)
}

// SuffixAt returns the stored key of slot i, without the page prefix. The
// slice points into the page: valid only until the page changes. ok is false if
// the slot does not describe a readable record. The caller checks i < SlotCount
func (p *Page) SuffixAt(i int) (suffix []byte, ok bool) {
	s := p.slotAt(i)
	if !recordOK(s, int(p.HeapStart())) {
		return nil, false
	}
	return p[s.off() : s.off()+s.keyLen()], true
}

// makeRoom ensures need bytes of free space, compacting if that is enough.
// It returns ErrNeedSplit when even compaction would not make room.
func (p *Page) makeRoom(need int, env *Env) error {
	if need <= p.freeSpace() {
		return nil
	}
	if need > p.freeSpace()+int(p.DeadBytes()) {
		return ErrNeedSplit
	}
	if env == nil || env.Scratch == nil || env.Scratch == p {
		return ErrNoScratch // packing a page into itself would corrupt it
	}
	p.Compact(env.Scratch)
	return nil
}

// checkRecord applies the size and key rules shared by Insert and Update.
// It runs before anything is encoded, because makeSlot silently masks
// oversized fields instead of rejecting them.
func (p *Page) checkRecord(suffix, val []byte) error {
	if len(suffix) > MaxKeyLen {
		return ErrTooLarge
	}
	if int64(len(val)) > MaxValueLen { // totalLen in the overflow stub is a uint32
		return ErrTooLarge
	}

	// A zero-length record on a page with no fences would start at PageSize,
	// which the 13-bit offset cannot hold. On such a page an empty suffix
	// means the full key is empty.
	if len(suffix)+len(val) == 0 && p.HeapStart() == PageSize {
		return ErrEmptyKey
	}
	return nil
}

// isOverflow reports whether a record this size must use an overflow chain.
func isOverflow(suffix, val []byte) bool {
	return len(suffix)+len(val) > MaxInlineRecord
}

// insertNew writes a new record at slot position idx. It is the part of
// Insert and Put that runs after the search.
//
//	In:   idx     insert position from seek on this page, with found = false
//	      suffix  the key with the page prefix already removed
//	      val     the value; bigger than MaxInlineRecord goes to an overflow chain
//	      env     Scratch if compaction may run; Pages if the value is overflow
//	Out:  nil, or:
//	      ErrNoPages    the value needs an overflow chain but env.Pages is nil
//	                    (checked before anything is touched)
//	      ErrNeedSplit  no room even after compaction; the page is unchanged
//	      ErrNoScratch  compaction was needed but env.Scratch is missing
//	      any error from env.Pages.Alloc while writing the chain
//	Rules:
//	  - The page must not change between the seek and this call: idx would
//	    point at the wrong place.
//	  - Compaction may run first. It keeps slot order, so idx stays valid.
//	  - Leaf space is checked before any chain is written, so ErrNeedSplit
//	    never wastes overflow pages. If writing the chain fails, the page is
//	    unchanged apart from a possible compaction.
//	  - Caller holds the page write lock.
func (p *Page) insertNew(idx int, suffix, val []byte, env *Env) error {
	ovf := isOverflow(suffix, val)
	inLeaf := len(val) // bytes the value takes inside the page
	if ovf {
		inLeaf = ovfStubSize
		if env == nil || env.Pages == nil {
			return ErrNoPages // checked before any compaction
		}
	}
	if err := p.makeRoom(len(suffix)+inLeaf+SlotSize, env); err != nil {
		return err
	}

	if !ovf {
		p.insertAt(idx, suffix, val, false)
		return nil
	}
	head, err := writeChain(env.Pages, val)
	if err != nil {
		return err // the page was not modified (beyond a possible compaction)
	}
	var stub [ovfStubSize]byte // on the stack: no allocation
	putStub(stub[:], head, uint32(len(val)))
	p.insertAt(idx, suffix, stub[:], true)
	return nil
}

// updateFound replaces the value of the record at slot idx. It is the part
// of Update and Put that runs after the search, and it handles all four
// cases of an old and a new value being inline or overflow.
//
//	In:   idx     slot position from seek on this page, with found = true
//	      old     the slot seek returned for idx (never re-load it: P55)
//	      suffix  the key with the page prefix already removed
//	      val     the new value
//	      env     Scratch if compaction may run; Pages if the old or the new
//	              value is overflow
//	Out:  nil, or:
//	      ErrNoPages    an overflow chain must be written or freed, but
//	                    env.Pages is nil (checked before anything is touched)
//	      ErrNeedSplit  the bigger value does not fit even after compaction;
//	                    the old value is still live and unchanged
//	      ErrNoScratch  compaction was needed but env.Scratch is missing
//	      any error from env.Pages.Alloc while writing the new chain
//	Rules:
//	  - If the new in-page bytes fit where the old ones were, they are
//	    written in place and only the leftover tail becomes dead bytes.
//	    Otherwise a fresh copy is carved, compacting first if needed.
//	  - The old stub is read before anything is overwritten, and the old
//	    chain is freed last, after the slot no longer points at it.
//	  - Caller holds the page write lock.
func (p *Page) updateFound(idx int, old slot, suffix, val []byte, env *Env) error {
	newOvf := isOverflow(suffix, val)
	newLen := len(val) // bytes the new value takes inside the page
	if newOvf {
		newLen = ovfStubSize
	}
	if (newOvf || old.ovf()) && (env == nil || env.Pages == nil) {
		return ErrNoPages // an old chain must be freed, or a new one written
	}

	// Read the old stub now: an in-place write or a compaction would destroy it.
	var oldHead, oldTotal uint32
	if old.ovf() {
		oldHead, oldTotal = readStub(p.valueBytes(old))
	}

	// Leaf space first, so ErrNeedSplit never wastes a freshly written chain.
	inPlace := newLen <= old.valLen()
	if !inPlace {
		if err := p.makeRoom(len(suffix)+newLen, env); err != nil {
			return err
		}
	}

	newBytes := val
	var stub [ovfStubSize]byte
	if newOvf {
		head, err := writeChain(env.Pages, val)
		if err != nil {
			return err // the old value is still live and untouched
		}
		putStub(stub[:], head, uint32(len(val)))
		newBytes = stub[:]
	}

	if inPlace {
		// No compaction ran on this path, so old's offset is still valid.
		copy(p[old.off()+old.keyLen():], newBytes)
		p.setSlotAt(idx, old.withValLen(newLen).withOvf(newOvf))
		p.SetDeadBytes(uint16(int(p.DeadBytes()) + old.valLen() - newLen))
	} else {
		p.replaceAt(idx, suffix, newBytes, newOvf)
	}

	if old.ovf() {
		// TODO(concurrency): retire via EBR instead of freeing immediately.
		freeChain(env.Pages, oldHead, overflowPages(int(oldTotal)))
	}
	return nil
}

// ---------- public operations ----------

// Insert adds a new key with its value. suffix is the key with the page
// prefix already removed.
//
// Values too big for the page go to an overflow chain (needs env.Pages).
// If the page lacks free space but deleted records would make room, it
// compacts first (needs env.Scratch). Leaf space is checked before any chain
// is written, so ErrNeedSplit never wastes overflow pages.
func (p *Page) Insert(suffix, val []byte, env *Env) error {
	if err := p.checkRecord(suffix, val); err != nil {
		return err
	}
	idx, found, ok := p.SeekSlot(suffix)
	if !ok {
		return ErrPageInvariant
	}
	if found {
		return ErrKeyExists
	}

	return p.insertNew(idx, suffix, val, env)
}

// Update replaces the value of an existing key. It handles all four
// combinations of an old and a new value being inline or overflow.
//
// If the new value's in-page bytes fit where the old ones were, it is written
// in place and only the leftover tail becomes dead. Otherwise a fresh copy is
// carved (compacting first if needed). An old overflow chain is freed last,
// after the slot no longer points at it.
//
// env.Scratch is needed if the page may need compaction; env.Pages is needed
// if the old or the new value is overflow.
func (p *Page) Update(suffix, val []byte, env *Env) error {
	if err := p.checkRecord(suffix, val); err != nil {
		return err
	}
	idx, old, found, ok := p.seek(suffix)
	if !ok {
		return ErrPageInvariant
	}
	if !found {
		return ErrKeyNotFound
	}
	return p.updateFound(idx, old, suffix, val, env)
}

// Delete removes a key. Its record becomes dead bytes (there are no
// tombstones). For an overflow record the slot is removed first and the
// chain freed afterwards, so a live slot never points at freed pages.
// env.Pages is needed only if the record is overflow.
func (p *Page) Delete(suffix []byte, env *Env) error {
	idx, old, found, ok := p.seek(suffix)
	if !ok {
		return ErrPageInvariant
	}
	if !found {
		return ErrKeyNotFound
	}

	var head, total uint32
	if old.ovf() {
		if env == nil || env.Pages == nil {
			return ErrNoPages // before the page is touched
		}
		head, total = readStub(p.valueBytes(old))
	}

	n := p.SlotCount()
	p.shiftLeft(idx, int(n))
	p.SetSlotCount(n - 1)
	p.SetDeadBytes(uint16(int(p.DeadBytes()) + old.keyLen() + old.valLen()))

	if old.ovf() {
		// TODO(concurrency): retire via EBR instead of freeing immediately.
		freeChain(env.Pages, head, overflowPages(int(total)))
	}
	return nil
}

// Get appends the value of suffix to dst and returns the result.
//
// The value is copied, so it stays valid after the page changes. If dst has
// enough capacity, Get allocates nothing; reuse the returned slice as the
// next dst. On error dst is returned unchanged. env.Pages is needed only for
// overflow values.
func (p *Page) Get(suffix, dst []byte, env *Env) ([]byte, error) {
	_, s, found, ok := p.seek(suffix)
	if !ok {
		return dst, ErrPageInvariant
	}
	if !found {
		return dst, ErrKeyNotFound
	}
	return p.valueOf(s, dst, env)
}

// Put stores val under suffix: it inserts the key if it is new, and
// replaces its value if it exists. It does one search, where Insert followed
// by Update would do two.
//
//	In:   suffix  the key with the page prefix already removed
//	      val     the value; bigger than MaxInlineRecord goes to an overflow chain
//	      env     Scratch if compaction may run; Pages for overflow values
//	Out:  inserted  true if the key was new, false if an existing value was
//	                replaced (always false when err != nil)
//	      err       nil, or ErrTooLarge / ErrEmptyKey (bad record),
//	                ErrPageInvariant (torn page view: retry),
//	                ErrNeedSplit (page full: the tree splits and retries),
//	                ErrNoPages, ErrNoScratch, or an allocation error
//	Rules:
//	  - On any error the page holds exactly what it held before, apart from
//	    a possible compaction, which never changes keys or values.
//	  - Caller holds the page write lock.
func (p *Page) Put(suffix, val []byte, env *Env) (inserted bool, err error) {
	if err := p.checkRecord(suffix, val); err != nil {
		return false, err
	}
	idx, old, found, ok := p.seek(suffix)
	if !ok {
		return false, ErrPageInvariant
	}
	if found {
		return false, p.updateFound(idx, old, suffix, val, env)
	}
	if err := p.insertNew(idx, suffix, val, env); err != nil {
		return false, err
	}
	return true, nil
}
