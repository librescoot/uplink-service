package telemetry

import (
	"context"
	"log"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/librescoot/uplink-service/internal/connection"
)

// Monitor watches Redis keys for changes and sends deltas
type Monitor struct {
	redisClient *redis.Client
	collector   *Collector
	connMgr     *connection.Manager
	debounce    time.Duration

	lastState   map[string]interface{}
	pendingChanges map[string]interface{}
	debounceTimer *time.Timer
}

// NewMonitor creates a new state monitor
func NewMonitor(redisClient *redis.Client, collector *Collector, connMgr *connection.Manager, debounce time.Duration) *Monitor {
	return &Monitor{
		redisClient: redisClient,
		collector:   collector,
		connMgr:     connMgr,
		debounce:    debounce,
		pendingChanges: make(map[string]interface{}),
	}
}

// Start begins monitoring Redis for changes
func (m *Monitor) Start(ctx context.Context) {
	log.Println("[Monitor] Starting Redis PUBSUB monitoring...")

	// Subscribe to notification channels for all monitored keys
	channels := []string{
		"vehicle", "battery:0", "battery:1", "aux-battery", "cb-battery",
		"engine-ecu", "gps", "internet", "modem", "power-manager",
		"keycard", "ble",
	}

	pubsub := m.redisClient.Subscribe(ctx, channels...)
	defer pubsub.Close()

	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			// Channel notification received (key was modified)
			m.handleChannelNotification(ctx, msg.Channel)
		}
	}
}

// handleChannelNotification processes a Redis channel notification
func (m *Monitor) handleChannelNotification(ctx context.Context, channel string) {
	// Channel name is the key name (e.g., "battery:0", "vehicle")
	log.Printf("[Monitor] Received notification on key: %s", channel)

	// Collect only the changed key's state (optimization)
	keyState, err := m.collector.CollectKeyState(ctx, channel)
	if err != nil {
		log.Printf("[Monitor] Failed to collect key state: %v", err)
		return
	}

	// Initialize lastState if needed (first notification)
	if m.lastState == nil {
		m.lastState = make(map[string]interface{})
	}

	// Compute changes for this key only
	changes := make(map[string]interface{})
	for fullKey, newVal := range keyState {
		oldVal, exists := m.lastState[fullKey]
		if !exists || !equal(oldVal, newVal) {
			if m.shouldNotifyKey(fullKey) {
				changes[fullKey] = newVal
			}
		}
		// Always update lastState
		m.lastState[fullKey] = newVal
	}

	if len(changes) == 0 {
		return
	}

	log.Printf("[Monitor] Computed %d changes", len(changes))

	// Add to pending changes
	for k, v := range changes {
		m.pendingChanges[k] = v
	}

	// Reset/start debounce timer
	if m.debounceTimer != nil {
		m.debounceTimer.Stop()
	}
	m.debounceTimer = time.AfterFunc(m.debounce, func() {
		m.flushChanges(ctx)
	})
}

// shouldNotifyKey returns whether we should send change notifications for this key
func (m *Monitor) shouldNotifyKey(fullKey string) bool {
	// Filter out noisy/transient fields
	if fullKey == "gps[timestamp]" {
		return false
	}

	// For batteries, only notify on charge/state/present
	if len(fullKey) >= 11 && (fullKey[:11] == "battery:0[" || fullKey[:11] == "battery:1[") {
		field := fullKey[11 : len(fullKey)-1] // extract field from battery:0[field]
		return field == "charge" || field == "state" || field == "present"
	}

	// For engine-ecu, only notify on speed/state
	if len(fullKey) >= 12 && fullKey[:11] == "engine-ecu[" {
		field := fullKey[11 : len(fullKey)-1]
		return field == "speed" || field == "state"
	}

	// All other fields are worth notifying
	return true
}

// flushChanges sends pending changes and clears the buffer
func (m *Monitor) flushChanges(ctx context.Context) {
	if len(m.pendingChanges) == 0 {
		return
	}

	log.Printf("[Monitor] Flushing %d pending changes", len(m.pendingChanges))

	if err := m.connMgr.SendChange(m.pendingChanges); err != nil {
		log.Printf("[Monitor] Failed to send changes: %v", err)
	}

	m.pendingChanges = make(map[string]interface{})
}

// equal compares two values for equality
func equal(a, b interface{}) bool {
	return a == b
}
