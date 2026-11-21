package gate

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// BenchmarkSpentSetMemory fills the cap and reports the heap it used.
// Run with: go test ./internal/gate -bench BenchmarkSpentSetMemory -benchtime 1x
func BenchmarkSpentSetMemory(b *testing.B) {
	const n = 1_000_000
	for i := 0; i < b.N; i++ {
		s := newSpentSet()
		now := time.Unix(1_800_000_000, 0)
		exp := now.Add(time.Hour)
		before := heapAlloc()
		for j := 0; j < n; j++ {
			id := spentKey("L402", fmt.Sprintf("%032x", j))
			if err := s.use(id, exp, now); err != nil {
				b.Fatal(err)
			}
		}
		after := heapAlloc()
		runtime.KeepAlive(s)
		delta := after - before
		b.ReportMetric(float64(delta)/float64(n), "B/entry")
		b.ReportMetric(float64(delta)/(1024*1024), "MiB")
		b.Logf("%d entries: Δ %d bytes (%.0f B/entry, %.1f MiB)", n, delta, float64(delta)/float64(n), float64(delta)/(1024*1024))
	}
}
