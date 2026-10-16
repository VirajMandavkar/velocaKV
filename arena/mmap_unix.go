//go:build linux || darwin

package arena

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
	"velocakv/page"
)

// mapSlab maps one anonymous, private, read-write slab of page.SlabBytes,
// outside the Go heap. If the base is not OS-page aligned, it unmaps the
// slab and returns an error.
func mapSlab() ([]byte, error) {
	data, err := syscall.Mmap(-1, 0, page.SlabBytes, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		return nil, fmt.Errorf("arena: mmap %d-byte slab: %w", page.SlabBytes, err)
	}
	const osPageAlign = 1 << 12
	base := uintptr(unsafe.Pointer(&data[0]))

	if base&(osPageAlign-1) != 0 {
		alignErr := fmt.Errorf("arena: base 0x%x, required alignment 0x%x", base, osPageAlign)
		if err := syscall.Munmap(data); err != nil {
			return nil, errors.Join(alignErr, fmt.Errorf("arena: munmap misaligned slab: %w", err))
		}
		return nil, alignErr
	}
	return data, nil
}

// unmapSlab releases a slab returned by mapSlab. The caller must
// guarantee nothing touches the slab afterwards. Calling it twice
// on the same slab returns an error.
func unmapSlab(b []byte) error {
	if err := syscall.Munmap(b); err != nil {
		return fmt.Errorf("arena: munmap slab: %w", err)
	}
	return nil
}
