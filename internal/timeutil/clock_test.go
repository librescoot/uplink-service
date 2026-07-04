package timeutil

import (
	"strings"
	"testing"
	"time"
)

func TestNowInvalidUntilValid(t *testing.T) {
	c := NewClock()
	ts := c.Now()
	if !strings.HasPrefix(ts, InvalidPrefix) {
		t.Fatalf("expected relative timestamp, got %q", ts)
	}
	c.MarkValid()
	ts = c.Now()
	if strings.HasPrefix(ts, InvalidPrefix) {
		t.Fatalf("expected absolute timestamp after MarkValid, got %q", ts)
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("timestamp not RFC3339: %v", err)
	}
}

func TestReproject(t *testing.T) {
	c := NewClock()
	rel := c.Now() // relative marker near offset 0
	c.MarkValid()

	abs, ok := c.Reproject(rel)
	if !ok {
		t.Fatalf("reproject failed")
	}
	parsed, err := time.Parse(time.RFC3339, abs)
	if err != nil {
		t.Fatalf("reprojected not RFC3339: %v", err)
	}
	// The reprojected instant should be very close to now.
	if d := time.Since(parsed); d < -2*time.Second || d > 2*time.Second {
		t.Errorf("reprojected time off by %v", d)
	}

	// Absolute timestamps pass through unchanged.
	fixed := "2026-01-02T15:04:05Z"
	out, ok := c.Reproject(fixed)
	if !ok || out != fixed {
		t.Errorf("absolute passthrough = %q ok=%v", out, ok)
	}
}
