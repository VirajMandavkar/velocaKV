//go:build !linux

package arena

// adviseHuge is a no-op where transparent huge pages don't exist.
func adviseHuge(b []byte) {}

func populate(b []byte) {}
