package main

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when the breaker is open and calls are short-circuited.
var ErrCircuitOpen = errors.New("circuit open")

type breakerState int

const (
	stateClosed breakerState = iota
	stateOpen
	stateHalfOpen
)

// CircuitBreaker is a simple count-based breaker (Closed → Open → Half-Open).
type CircuitBreaker struct {
	mu               sync.Mutex
	state            breakerState
	failures         int
	openedAt         time.Time
	halfOpenInFlight int

	failureThreshold int
	openDuration     time.Duration
	halfOpenMax      int
}

func NewCircuitBreaker(failureThreshold int, openDuration time.Duration, halfOpenMax int) *CircuitBreaker {
	if failureThreshold < 1 {
		failureThreshold = 5
	}
	if openDuration <= 0 {
		openDuration = 30 * time.Second
	}
	if halfOpenMax < 1 {
		halfOpenMax = 1
	}
	return &CircuitBreaker{
		state:            stateClosed,
		failureThreshold: failureThreshold,
		openDuration:     openDuration,
		halfOpenMax:      halfOpenMax,
	}
}

// Execute runs fn if the circuit allows it.
func (cb *CircuitBreaker) Execute(fn func() error) error {
	if err := cb.before(); err != nil {
		return err
	}
	err := fn()
	cb.after(err)
	return err
}

func (cb *CircuitBreaker) before() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case stateOpen:
		if time.Since(cb.openedAt) < cb.openDuration {
			return ErrCircuitOpen
		}
		cb.state = stateHalfOpen
		cb.halfOpenInFlight = 0
		fallthrough
	case stateHalfOpen:
		if cb.halfOpenInFlight >= cb.halfOpenMax {
			return ErrCircuitOpen
		}
		cb.halfOpenInFlight++
	}
	return nil
}

func (cb *CircuitBreaker) after(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err == nil {
		cb.failures = 0
		cb.state = stateClosed
		cb.halfOpenInFlight = 0
		return
	}

	cb.failures++
	if cb.state == stateHalfOpen || cb.failures >= cb.failureThreshold {
		cb.state = stateOpen
		cb.openedAt = time.Now()
		cb.halfOpenInFlight = 0
	}
}

// State returns a human-readable state name (for /healthz or logs).
func (cb *CircuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case stateOpen:
		if time.Since(cb.openedAt) >= cb.openDuration {
			return "half_open"
		}
		return "open"
	case stateHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}
