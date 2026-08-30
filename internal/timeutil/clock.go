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

// The image-seeded RTC is untrusted until NTP succeeds. Relative timestamps use
// this prefix and a process-monotonic offset, never a plausible-looking wall time.
const InvalidPrefix = "INVALID_RELATIVE:"

type Clock struct {
	mu        sync.RWMutex
	anchor    time.Time
	valid     bool
	sessionID string
}

func NewClock() *Clock {
	return &Clock{
		anchor:    time.Now(),
		sessionID: newSessionID(),
	}
}

func (c *Clock) SessionID() string {
	return c.sessionID
}

func (c *Clock) Valid() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.valid
}

func (c *Clock) MarkValid() {
	c.mu.Lock()
	c.valid = true
	c.mu.Unlock()
}

func (c *Clock) Now() string {
	if c.Valid() {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s%.3f", InvalidPrefix, time.Since(c.anchor).Seconds())
}

// Reproject may only convert offsets from this process's monotonic anchor.
func (c *Clock) Reproject(ts string) (string, bool) {
	if !strings.HasPrefix(ts, InvalidPrefix) {
		return ts, true
	}
	offset, err := strconv.ParseFloat(strings.TrimPrefix(ts, InvalidPrefix), 64)
	if err != nil {
		return ts, false
	}
	elapsedNow := time.Since(c.anchor).Seconds()

	// Offset is anchored to the process's monotonic clock, not wall time.
	wall := time.Now().Add(-time.Duration((elapsedNow - offset) * float64(time.Second)))
	return wall.UTC().Format(time.RFC3339), true
}

func IsRelative(ts string) bool {
	return strings.HasPrefix(ts, InvalidPrefix)
}

// A validated NTP response establishes timestamp validity; setting system time
// remains the caller's responsibility.
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

		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}
