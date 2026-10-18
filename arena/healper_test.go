package arena

// grow maps one more slab. Test helper: production code grows through growTo.
func (a *Arena) grow() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.growLocked()
}
