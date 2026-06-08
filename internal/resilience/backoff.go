// Package resilience holds reusable building blocks for connection retry logic
// shared across transports (simulator TCP integrations today, BLE in future).
package resilience

import "time"

// Backoff computes an exponentially increasing delay between retry attempts,
// capped at Max. It doubles on each failure and resets to Initial on success.
//
// Backoff is not safe for concurrent use; callers should guard it with the same
// lock that protects the surrounding connection state.
type Backoff struct {
	Initial time.Duration
	Max     time.Duration
	current time.Duration
}

// NewBackoff returns a Backoff starting at initial and capped at max.
func NewBackoff(initial, max time.Duration) *Backoff {
	return &Backoff{Initial: initial, Max: max, current: initial}
}

// Current returns the delay to enforce before the next attempt.
func (b *Backoff) Current() time.Duration {
	if b.current <= 0 {
		b.current = b.Initial
	}
	return b.current
}

// Next doubles the delay (capped at Max) and returns the new value. Call it
// after a failed attempt.
func (b *Backoff) Next() time.Duration {
	next := b.Current() * 2
	if b.Max > 0 && next > b.Max {
		next = b.Max
	}
	b.current = next
	return next
}

// Reset returns the delay to its initial value. Call it after a successful
// attempt.
func (b *Backoff) Reset() {
	b.current = b.Initial
}
