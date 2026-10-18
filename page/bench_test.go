package page

import (
	"fmt"
	"math/rand"
	"testing"
)

func BenchmarkFillLeaf(b *testing.B) {
	keys := make([][]byte, 4096)
	for i := range keys {
		keys[i] = fmt.Appendf(nil, "key-%08d", i)
	}
	rand.New(rand.NewSource(1)).Shuffle(len(keys), func(i, j int) {
		keys[i], keys[j] = keys[j], keys[i]
	})
	p := newTestPage()
	n := 0
	b.ReportAllocs()
	b.ResetTimer() // don't count building the keys
	for i := 0; i < b.N; i++ {
		_ = p.Format(KindLeaf, nil, nil)
		n = 0
		for _, k := range keys {
			if err := p.Insert(k, k, nil); err != nil {
				break // page full
			}
			n++
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/insert")
	b.ReportMetric(float64(n), "records")
}

// BenchmarkDeleteLeaf fills a leaf (untimed), then deletes every key in a
// different random order (timed). ns/delete is the number to compare.
func BenchmarkDeleteLeaf(b *testing.B) {
	keys := make([][]byte, 4096)
	for i := range keys {
		keys[i] = fmt.Appendf(nil, "key-%08d", i)
	}
	r := rand.New(rand.NewSource(1))
	r.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })

	p := newTestPage()
	var in [][]byte // the keys that fit
	_ = p.Format(KindLeaf, nil, nil)
	for _, k := range keys {
		if p.Insert(k, k, nil) != nil {
			break
		}
		in = append(in, k)
	}
	order := append([][]byte(nil), in...)
	r.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		_ = p.Format(KindLeaf, nil, nil)
		for _, k := range in {
			_ = p.Insert(k, k, nil)
		}
		b.StartTimer()
		for _, k := range order {
			if err := p.Delete(k, nil); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(order)), "ns/delete")
}

func BenchmarkCompact(b *testing.B) {
	keys := make([][]byte, 4096)
	for i := range keys {
		keys[i] = fmt.Appendf(nil, "key-%08d", i)
	}
	rand.New(rand.NewSource(1)).Shuffle(len(keys), func(i, j int) {
		keys[i], keys[j] = keys[j], keys[i]
	})

	// Build the template ONCE: a full leaf, then every other key deleted.
	template := newTestPage()
	_ = template.Format(KindLeaf, nil, nil)
	var in [][]byte
	for _, k := range keys {
		if template.Insert(k, k, nil) != nil {
			break // page full
		}
		in = append(in, k)
	}
	for j := 0; j < len(in); j += 2 {
		if err := template.Delete(in[j], nil); err != nil {
			b.Fatal(err)
		}
	}

	p, scratch := newTestPage(), newTestPage()
	b.ReportAllocs()

	for b.Loop() {
		*p = *template     // fresh half-dead page (one 8 KiB copy, timed)
		p.Compact(scratch) // the work we measure
	}
	b.ReportMetric(float64(template.SlotCount()), "live")
}

// BenchmarkCompactCopyOnly is the 8 KiB copy alone. Subtract it from
// BenchmarkCompact to get the cost of Compact by itself.
func BenchmarkCompactCopyOnly(b *testing.B) {
	template, p := newTestPage(), newTestPage()
	for b.Loop() {
		*p = *template
	}
}

type seekBenchRecord struct {
	suffix []byte
	value  []byte
}

func seekBenchPage(b *testing.B) (*Page, []seekBenchRecord) {
	const firstKey = 412380
	const keyCount = 180
	low := []byte("key-0041238")
	high := []byte("key-0041256")

	p := newTestPage()
	if err := p.Format(KindLeaf, low, high); err != nil {
		b.Fatal(err)
	}
	records := make([]seekBenchRecord, keyCount)
	for i := range keyCount {
		key := fmt.Appendf(nil, "key-%08d", firstKey+i)
		records[i] = seekBenchRecord{
			suffix: append([]byte(nil), key[p.PrefixLen():]...),
			value:  key,
		}
		if err := p.Insert(records[i].suffix, records[i].value, nil); err != nil {
			b.Fatalf("Insert(%q): %v", records[i].suffix, err)
		}
	}
	rand.New(rand.NewSource(1)).Shuffle(len(records), func(i, j int) {
		records[i], records[j] = records[j], records[i]
	})
	return p, records
}

func BenchmarkSeek(b *testing.B) {
	p, records := seekBenchPage(b)
	foundCount := 0
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, found, ok := p.SeekSlot(records[i%len(records)].suffix)
		if !ok {
			b.Fatal("SeekSlot reported an invalid page")
		}
		if found {
			foundCount++
		}
	}
	if foundCount != b.N {
		b.Fatalf("found %d keys, want %d", foundCount, b.N)
	}
}

func BenchmarkUpdateTwoSeeks(b *testing.B) {
	p, records := seekBenchPage(b)
	b.ReportAllocs()

	for i := 0; b.Loop(); i++ {
		record := records[i%len(records)]
		if err := p.Insert(record.suffix, record.value, nil); err != ErrKeyExists {
			b.Fatalf("Insert(%q) error = %v, want %v", record.suffix, err, ErrKeyExists)
		}
		if err := p.Update(record.suffix, record.value, nil); err != nil {
			b.Fatalf("Update(%q): %v", record.suffix, err)
		}
	}
}

func BenchmarkPutExisting(b *testing.B) {
	p, records := seekBenchPage(b)
	b.ReportAllocs()

	for i := 0; b.Loop(); i++ {
		record := records[i%len(records)]
		inserted, err := p.Put(record.suffix, record.value, nil)
		if err != nil || inserted {
			b.Fatalf("Put(%q) = (%v, %v), want (false, nil)", record.suffix, inserted, err)
		}
	}
}

func BenchmarkSeekNoFences(b *testing.B) {
	const firstKey = 412380
	const keyCount = 180

	p := newTestPage()
	if err := p.Format(KindLeaf, nil, nil); err != nil {
		b.Fatal(err)
	}
	keys := make([][]byte, keyCount)
	for i := range keyCount {
		keys[i] = fmt.Appendf(nil, "key-%08d", firstKey+i)
		if err := p.Insert(keys[i], keys[i], nil); err != nil {
			b.Fatalf("Insert(%q): %v", keys[i], err)
		}
	}
	rand.New(rand.NewSource(1)).Shuffle(len(keys), func(i, j int) {
		keys[i], keys[j] = keys[j], keys[i]
	})

	foundCount := 0
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, found, ok := p.SeekSlot(keys[i%len(keys)])
		if !ok {
			b.Fatal("SeekSlot reported an invalid page")
		}
		if found {
			foundCount++
		}
	}
	if foundCount != b.N {
		b.Fatalf("found %d keys, want %d", foundCount, b.N)
	}
}
