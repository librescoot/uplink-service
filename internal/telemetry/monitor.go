package telemetry

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	ipc "github.com/librescoot/redis-ipc"

	"github.com/librescoot/uplink-service/internal/connection"
)

// Priority defines the flush deadline for telemetry fields
type Priority int

const (
	Immediate Priority = iota // 1s deadline (critical state)
	Quick                     // 5s deadline (GPS)
	Medium                    // 10s deadline (default)
	Slow                      // 15min deadline (low priority: battery, signal)
)

var priorityDeadlines = map[Priority]time.Duration{
	Immediate: 1 * time.Second,
	Quick:     5 * time.Second,
	Medium:    10 * time.Second,
	Slow:      15 * time.Minute,
}

var priorityNames = map[Priority]string{
	Immediate: "1s",
	Quick:     "5s",
	Medium:    "10s",
	Slow:      "15m",
}

// Field-specific priority mappings
var fieldPriorities = map[string]Priority{
	"vehicle[state]":                 Immediate,
	"vehicle[seatbox:lock]":          Immediate,
	"vehicle[handlebar:lock-sensor]": Immediate,
	"vehicle[blinker:state]":         Immediate,
	"power-manager[state]":           Immediate,
	"aux-battery[voltage]":           Slow,
	"cb-battery[cell-voltage]":       Slow,
	"cb-battery[current]":            Slow,
	"cb-battery[remaining-capacity]": Slow,
	"cb-battery[time-to-full]":       Slow,
	"ble[last-update]":               Slow,
}

// Hash-level priority mappings
var hashPriorities = map[string]Priority{
	"gps": Quick,
}

// Monitor watches Redis keys for changes and sends deltas
type Monitor struct {
	client    *ipc.Client
	collector *Collector
	connMgr   *connection.Manager

	mu       sync.Mutex
	watchers []*ipc.HashWatcher

	// Per-priority configuration and state
	priorityDeadlines map[Priority]time.Duration
	priorityPending   map[Priority]map[string]any // priority -> hash -> field -> value
	priorityTimers    map[Priority]*time.Timer

	lastValues map[string]string
}

// NewMonitor creates a new state monitor
func NewMonitor(client *ipc.Client, collector *Collector, connMgr *connection.Manager, debounce time.Duration) *Monitor {
	return &Monitor{
		client:            client,
		collector:         collector,
		connMgr:           connMgr,
		priorityDeadlines: priorityDeadlines,
		priorityPending: map[Priority]map[string]any{
			Immediate: make(map[string]any),
			Quick:     make(map[string]any),
			Medium:    make(map[string]any),
			Slow:      make(map[string]any),
		},
		priorityTimers: make(map[Priority]*time.Timer),
		lastValues:     make(map[string]string),
	}
}

// InitializeBaseline sets the initial values from a state snapshot
func (m *Monitor) InitializeBaseline(state map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for hash, fields := range state {
		if fieldMap, ok := fields.(map[string]any); ok {
			for field, value := range fieldMap {
				fullKey := hash + "[" + field + "]"
				if strVal, ok := value.(string); ok {
					m.lastValues[fullKey] = strVal
				}
			}
		}
	}
	log.Printf("[Monitor] Initialized baseline with %d field values", len(m.lastValues))
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
		// No debounce at HashWatcher level - Monitor handles priority-based deadlines
		watcher.OnAny(func(field, value string) error {
			return m.handleFieldChange(channel, field, value)
		})
		watcher.Start()
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

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if value actually changed
	if m.lastValues[fullKey] == value {
		return nil
	}
	m.lastValues[fullKey] = value

	// Determine priority for this field
	priority := m.getFieldPriority(hash, field)

	// Add to priority-specific pending changes as nested structure
	pending := m.priorityPending[priority]
	if pending[hash] == nil {
		pending[hash] = make(map[string]any)
	}
	pending[hash].(map[string]any)[field] = value

	// Start deadline timer if not already running (deadline semantics - no reset!)
	if m.priorityTimers[priority] == nil {
		deadline := m.priorityDeadlines[priority]
		m.priorityTimers[priority] = time.AfterFunc(deadline, func() {
			m.flushPriority(priority)
		})
	}

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

// getFieldPriority determines the flush priority for a field
func (m *Monitor) getFieldPriority(hash, field string) Priority {
	fullKey := hash + "[" + field + "]"

	// Check exact field match
	if prio, ok := fieldPriorities[fullKey]; ok {
		return prio
	}

	// Check hash-level priority
	if prio, ok := hashPriorities[hash]; ok {
		return prio
	}

	// Default priority
	return Medium
}

// flushPriority sends pending changes for a specific priority and clears the buffer
func (m *Monitor) flushPriority(priority Priority) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear the timer reference
	m.priorityTimers[priority] = nil

	// Get pending changes for this priority
	pending := m.priorityPending[priority]
	if len(pending) == 0 {
		return
	}

	// Build change summary for logging
	var changes []string
	for hash, fields := range pending {
		if fieldMap, ok := fields.(map[string]any); ok {
			for field := range fieldMap {
				changes = append(changes, hash+"["+field+"]")
			}
		}
	}

	// Sort for consistent logging
	sort.Strings(changes)

	log.Printf("[Monitor] Flush (%s): %v", priorityNames[priority], changes)

	if err := m.connMgr.SendChange(pending); err != nil {
		log.Printf("[Monitor] Failed to send changes: %v", err)
	}

	// Clear pending changes for this priority
	m.priorityPending[priority] = make(map[string]any)
}
