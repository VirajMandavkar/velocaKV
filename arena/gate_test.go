package arena

import "testing"

// BenchmarkWorkerAllocFree is the phase gate: concurrent Alloc/Free
// through Worker must report 0 allocs/op.
func BenchmarkWorkerAllocFree(b *testing.B) {
	a, err := New(1 << 30)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = a.Close() })

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		w, err := a.NewWorker() // one worker per goroutine
		if err != nil {
			b.Error(err)
			return
		}
		defer w.Close()
		for pb.Next() {
			id, _, err := w.Alloc()
			if err != nil {
				b.Error(err)
				return
			}
			w.Free(id)
		}
	})
}
