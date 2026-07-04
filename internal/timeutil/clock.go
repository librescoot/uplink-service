// Package timeutil provides monotonic-anchored, NTP-gated timestamps.
//
// The embedded target's real-time clock is seeded with the firmware image build
// timestamp at first boot, so a plausible-looking wall-clock year is NOT proof
// the clock is correct. We therefore treat time as invalid until an explicit NTP
// synchronisation succeeds. Until then, timestamps are emitted as a relative
// offset from a monotonic anchor and are reprojected to wall-clock once time
// becomes valid.
package timeutil

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/beevik/ntp"
)

// InvalidPrefix marks a timestamp whose absolute wall-clock time is not yet
// known; the suffix is the number of seconds elapsed (monotonic) since the
// process's anchor.
const InvalidPrefix = "INVALID_RELATIVE:"

// Clock tracks time validity and produces timestamps.
type Clock struct {
	mu        sync.RWMutex
	anchor    time.Time // captured at construction, carries a monotonic reading
	valid     bool
	sessionID string
}

// NewClock creates a clock anchored at the current monotonic instant. The clock
// starts invalid until MarkValid or SyncOnce succeeds.
func NewClock() *Clock {
	return &Clock{
		anchor:    time.Now(),
		sessionID: newSessionID(),
	}
}

// SessionID returns a random identifier unique to this process lifetime. It is
// used to discard buffered relative timestamps that cannot be reprojected
// because they originated in a previous session.
func (c *Clock) SessionID() string {
	return c.sessionID
}

// Valid reports whether wall-clock time has been established via NTP.
func (c *Clock) Valid() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.valid
}

// MarkValid records that wall-clock time is now trustworthy.
func (c *Clock) MarkValid() {
	c.mu.Lock()
	c.valid = true
	c.mu.Unlock()
}

// Now returns a timestamp string. When time is valid it is an RFC3339 UTC
// instant; otherwise it is a monotonic-relative marker.
func (c *Clock) Now() string {
	if c.Valid() {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s%.3f", InvalidPrefix, time.Since(c.anchor).Seconds())
}

// Reproject converts a relative-offset timestamp string produced by an earlier
// Now() call into an absolute RFC3339 instant, using the current (now valid)
// wall clock and the monotonic anchor. Non-relative strings are returned
// unchanged. Returns ok=false when the input is malformed.
func (c *Clock) Reproject(ts string) (string, bool) {
	if !strings.HasPrefix(ts, InvalidPrefix) {
		return ts, true
	}
	offset, err := strconv.ParseFloat(strings.TrimPrefix(ts, InvalidPrefix), 64)
	if err != nil {
		return ts, false
	}
	elapsedNow := time.Since(c.anchor).Seconds()
	// The event occurred (elapsedNow - offset) seconds before now.
	wall := time.Now().Add(-time.Duration((elapsedNow - offset) * float64(time.Second)))
	return wall.UTC().Format(time.RFC3339), true
}

// IsRelative reports whether a timestamp string is a monotonic-relative marker.
func IsRelative(ts string) bool {
	return strings.HasPrefix(ts, InvalidPrefix)
}

// SyncOnce queries the given NTP server and, on success, marks the clock valid.
// It returns the queried time and any error. Setting the system clock is left to
// the caller/environment; validity is established from a successful query, not
// from a successful set.
func (c *Clock) SyncOnce(server string) (time.Time, error) {
	resp, err := ntp.Query(server)
	if err != nil {
		return time.Time{}, err
	}
	if err := resp.Validate(); err != nil {
		return time.Time{}, err
	}
	c.MarkValid()
	return time.Now().Add(resp.ClockOffset), nil
}

func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to the anchor's nanoseconds; uniqueness is best-effort.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}
