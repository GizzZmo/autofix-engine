package main

import (
	"errors"
	"testing"
	"time"
)

func TestCircuitBreakerOpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker(3, 50*time.Millisecond, 1)
	fail := errors.New("boom")

	for i := 0; i < 3; i++ {
		err := cb.Execute(func() error { return fail })
		if err != fail {
			t.Fatalf("attempt %d: want boom, got %v", i, err)
		}
	}

	err := cb.Execute(func() error { return nil })
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if cb.State() != "open" {
		t.Fatalf("expected state open, got %s", cb.State())
	}
}

func TestCircuitBreakerHalfOpenThenClose(t *testing.T) {
	cb := NewCircuitBreaker(1, 20*time.Millisecond, 1)
	_ = cb.Execute(func() error { return errors.New("fail") })

	if cb.State() != "open" {
		t.Fatalf("expected open, got %s", cb.State())
	}

	time.Sleep(25 * time.Millisecond)

	err := cb.Execute(func() error { return nil })
	if err != nil {
		t.Fatalf("half-open success should close circuit, got %v", err)
	}
	if cb.State() != "closed" {
		t.Fatalf("expected closed, got %s", cb.State())
	}
}
