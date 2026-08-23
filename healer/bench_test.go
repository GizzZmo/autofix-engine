package main

import (
	"fmt"
	"testing"
)

// HTML snippets for soft-404 heuristic benchmarks.
var (
	benchHealthyHTML = []byte(`<!DOCTYPE html><html><head><title>Welcome</title></head><body><h1>Hello</h1><p>All good here.</p></body></html>`)
	benchSoft404HTML = []byte(`<!DOCTYPE html><html><head><title>404 Not Found</title></head><body><h1>404</h1><p>The requested URL was not found on this server.</p></body></html>`)
	benchLargeHTML   []byte
)

func init() {
	// ~8 KiB body matching the soft-404 scan window
	buf := make([]byte, 0, 8192)
	buf = append(buf, []byte(`<!DOCTYPE html><html><head><title>Docs</title></head><body>`)...)
	for len(buf) < 8000 {
		buf = append(buf, []byte(`<p>lorem ipsum dolor sit amet</p>`)...)
	}
	buf = append(buf, []byte(`</body></html>`)...)
	benchLargeHTML = buf
}

func BenchmarkLooksLikeSoft404_Healthy(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = looksLikeSoft404(benchHealthyHTML, "text/html; charset=utf-8")
	}
}

func BenchmarkLooksLikeSoft404_Soft404(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = looksLikeSoft404(benchSoft404HTML, "text/html")
	}
}

func BenchmarkLooksLikeSoft404_LargeHealthy(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = looksLikeSoft404(benchLargeHTML, "text/html")
	}
}

func BenchmarkKeyFor(b *testing.B) {
	url := "https://example.com/path/to/resource?q=1&x=2"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = keyFor(url)
	}
}

func BenchmarkDiscoveryQueueEnqueue(b *testing.B) {
	q := NewDiscoveryQueue(b.N + 10)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Enqueue(fmt.Sprintf("https://example.com/%d", i))
	}
}

func BenchmarkDiscoveryQueueEnqueueDuplicate(b *testing.B) {
	q := NewDiscoveryQueue(16)
	url := "https://example.com/same"
	q.Enqueue(url)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Enqueue(url)
	}
}
