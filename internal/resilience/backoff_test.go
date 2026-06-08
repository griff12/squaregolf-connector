package resilience

import (
	"testing"
	"time"
)

func TestBackoffDoublesAndCaps(t *testing.T) {
	b := NewBackoff(5*time.Second, 30*time.Second)

	if got := b.Current(); got != 5*time.Second {
		t.Fatalf("initial Current() = %v, want 5s", got)
	}

	want := []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := b.Next(); got != w {
			t.Errorf("Next() call %d = %v, want %v", i+1, got, w)
		}
	}
}

func TestBackoffReset(t *testing.T) {
	b := NewBackoff(5*time.Second, 30*time.Second)
	b.Next()
	b.Next()

	b.Reset()
	if got := b.Current(); got != 5*time.Second {
		t.Fatalf("after Reset, Current() = %v, want 5s", got)
	}
}

func TestBackoffZeroCurrentFallsBackToInitial(t *testing.T) {
	b := &Backoff{Initial: 2 * time.Second, Max: 8 * time.Second}
	if got := b.Current(); got != 2*time.Second {
		t.Fatalf("Current() with zero current = %v, want 2s", got)
	}
}

func TestBackoffNoMaxDoesNotCap(t *testing.T) {
	b := NewBackoff(1*time.Second, 0)
	if got := b.Next(); got != 2*time.Second {
		t.Fatalf("Next() with no max = %v, want 2s", got)
	}
	if got := b.Next(); got != 4*time.Second {
		t.Fatalf("Next() with no max = %v, want 4s", got)
	}
}
