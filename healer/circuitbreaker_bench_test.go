package main

import (
	"errors"
	"testing"
	"time"
)

func BenchmarkCircuitBreakerExecuteSuccess(b *testing.B) {
	cb := NewCircuitBreaker(5, time.Minute, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cb.Execute(func() error { return nil })
	}
}

func BenchmarkCircuitBreakerExecuteFailure(b *testing.B) {
	// High threshold so the breaker stays closed for the whole run.
	cb := NewCircuitBreaker(b.N+10, time.Minute, 1)
	fail := errors.New("boom")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cb.Execute(func() error { return fail })
	}
}

func BenchmarkCircuitBreakerOpenShortCircuit(b *testing.B) {
	cb := NewCircuitBreaker(1, time.Hour, 1)
	_ = cb.Execute(func() error { return errors.New("trip") })
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cb.Execute(func() error { return nil })
	}
}

func BenchmarkCircuitBreakerParallel(b *testing.B) {
	cb := NewCircuitBreaker(1_000_000, time.Minute, 8)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = cb.Execute(func() error { return nil })
		}
	})
}

func BenchmarkCircuitBreakerState(b *testing.B) {
	cb := NewCircuitBreaker(5, time.Minute, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cb.State()
	}
}
