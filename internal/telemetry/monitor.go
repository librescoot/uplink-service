package telemetry

import (
	"context"
	"log"
	"time"

	ipc "github.com/librescoot/redis-ipc"

	"github.com/librescoot/uplink-service/internal/connection"
)

// Monitor watches Redis keys for changes and sends deltas
type Monitor struct {
	client    *ipc.Client
	collector *Collector
	connMgr   *connection.Manager
	debounce  time.Duration

	watchers       []*ipc.HashWatcher
	pendingChanges map[string]interface{}
	debounceTimer  *time.Timer
}

// NewMonitor creates a new state monitor
func NewMonitor(client *ipc.Client, collector *Collector, connMgr *connection.Manager, debounce time.Duration) *Monitor {
	return &Monitor{
		client:         client,
		collector:      collector,
		connMgr:        connMgr,
		debounce:       debounce,
		pendingChanges: make(map[string]interface{}),
	}
}

// Start begins monitoring Redis for changes
func (m *Monitor) Start(ctx context.Context) {
	log.Println("[Monitor] Starting Redis PUBSUB monitoring with HashWatchers...")

	// Create HashWatcher for each monitored key
	channels := []string{
		"vehicle", "battery:0", "battery:1", "aux-battery", "cb-battery",
		"engine-ecu", "gps", "internet", "modem", "power-manager",
		"keycard", "ble",
	}

	for _, channel := range channels {
		watcher := m.client.NewHashWatcher(channel)
		watcher.SetDebounce(m.debounce)
		watcher.OnAny(func(field, value string) error {
			return m.handleFieldChange(channel, field, value)
		})
		watcher.StartWithSync(ctx)
		m.watchers = append(m.watchers, watcher)
	}

	log.Printf("[Monitor] Started %d HashWatchers", len(m.watchers))

	// Block until context is done
	<-ctx.Done()

	// Stop all watchers
	for _, watcher := range m.watchers {
		watcher.Stop()
	}
}

// handleFieldChange processes a field change from HashWatcher
func (m *Monitor) handleFieldChange(hash, field, value string) error {
	fullKey := hash + "[" + field + "]"

	// Filter out noisy fields
	if !m.shouldNotifyKey(fullKey) {
		return nil
	}

	log.Printf("[Monitor] Change: %s = %s", fullKey, value)

	// Add to pending changes
	m.pendingChanges[fullKey] = value

	// Reset/start debounce timer
	if m.debounceTimer != nil {
		m.debounceTimer.Stop()
	}
	m.debounceTimer = time.AfterFunc(m.debounce, func() {
		m.flushChanges()
	})

	return nil
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
func (m *Monitor) flushChanges() {
	if len(m.pendingChanges) == 0 {
		return
	}

	log.Printf("[Monitor] Flushing %d pending changes", len(m.pendingChanges))

	if err := m.connMgr.SendChange(m.pendingChanges); err != nil {
		log.Printf("[Monitor] Failed to send changes: %v", err)
	}

	m.pendingChanges = make(map[string]interface{})
}
