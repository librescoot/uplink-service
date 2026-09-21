package commands

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// tripCounterCommand mirrors trip-service's scooter:trip request envelope.
// trip-service rejects deadlines already past or more than a minute out, so
// the payload must be pushed immediately after it is built.
type tripCounterCommand struct {
	ID        string `json:"id"`
	Op        string `json:"op"`
	Source    string `json:"source"`
	ExpiresAt int64  `json:"expires-at"`
}

func (h *Handler) tripReset() (map[string]any, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(buf)

	payload, err := json.Marshal(tripCounterCommand{
		ID:        id,
		Op:        "counter.reset",
		Source:    "uplink",
		ExpiresAt: time.Now().Add(30 * time.Second).UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	if err := h.sendCommand("scooter:trip", string(payload)); err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}
