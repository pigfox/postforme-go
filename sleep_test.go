package postforme

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSleepCtx covers the production wait both ways. Every other test replaces
// this seam with an instant stub, so without this the real implementation —
// the one that runs in production — would never execute in the suite at all.
func TestSleepCtx(t *testing.T) {
	t.Run("returns after the delay", func(t *testing.T) {
		start := time.Now()
		if err := sleepCtx(t.Context(), 5*time.Millisecond); err != nil {
			t.Fatalf("sleepCtx: %v", err)
		}
		if elapsed := time.Since(start); elapsed < 5*time.Millisecond {
			t.Errorf("returned after %v, want at least 5ms", elapsed)
		}
	})

	t.Run("aborts on a canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		start := time.Now()
		err := sleepCtx(ctx, time.Hour)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		// The point of the select: an hour's backoff must not outlive a
		// canceled request.
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("waited %v on a canceled context", elapsed)
		}
	})
}

// TestBackoffCapsWhenTheBaseAlreadyExceedsTheCap covers the post-loop clamp: a
// base larger than maxBackoff on the very first attempt never enters the
// doubling loop, so only the trailing check can catch it.
func TestBackoffCapsWhenTheBaseAlreadyExceedsTheCap(t *testing.T) {
	if got := backoffFor(time.Hour, 1); got != maxBackoff {
		t.Fatalf("backoffFor(1h, 1) = %v, want %v", got, maxBackoff)
	}
}
