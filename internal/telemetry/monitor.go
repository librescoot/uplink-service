package telemetry

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	ipc "github.com/librescoot/redis-ipc"

	"github.com/librescoot/uplink-service/internal/connection"
)

// Priority defines the flush deadline for telemetry fields
type Priority int

const (
	Immediate Priority = iota
	Quick
	Medium
	Slow
)

var priorityDeadlines = map[Priority]time.Duration{
	Immediate: 1 * time.Second,
	Quick:     5 * time.Second,
	Medium:    60 * time.Second,
	Slow:      15 * time.Minute,
}

var priorityNames = map[Priority]string{
	Immediate: "Immediate",
	Quick:     "Quick",
	Medium:    "Medium",
	Slow:      "Slow",
}

// Field-specific priority mappings
var fieldPriorities = map[string]Priority{
	"vehicle[state]":                 Immediate,
	"vehicle[seatbox:lock]":          Immediate,
	"vehicle[handlebar:lock-sensor]": Immediate,
	"vehicle[blinker:state]":         Immediate,
	"vehicle[main-power]":            Immediate,
	"vehicle[engine-power]":          Immediate,
	"power-manager[state]":           Immediate,
	"ota[status]":                    Immediate,
	"alarm[status]":                  Immediate,
	"alarm[alarm-active]":            Immediate,
	"aux-battery[voltage]":           Slow,
	"cb-battery[cell-voltage]":       Slow,
	"cb-battery[current]":            Slow,
	"cb-battery[remaining-capacity]": Slow,
	"cb-battery[time-to-full]":       Slow,
	"ble[last-update]":               Slow,
}

// Hash-level priority mappings
var hashPriorities = map[string]Priority{
	"gps":       Quick,
	"battery:0": Quick,
	"battery:1": Quick,
}

// noisyFields never trigger a flush on their own. Their current values are still
// included whenever a snapshot or delta is sent (the publisher recollects fresh
// state), but their constant churn must not drive traffic.
var noisyFields = map[string]bool{
	"gps[timestamp]":           true,
	"gps[latitude]":            true,
	"gps[longitude]":           true,
	"gps[altitude]":            true,
	"gps[course]":              true,
	"internet[signal-quality]": true,
}

// quantizationBuckets defines, per fully-qualified field, the granularity at
// which a change is considered significant enough to schedule a flush. Sensor
// dither below the bucket size is ignored for triggering purposes.
var quantizationBuckets = map[string]float64{
	"battery:0[voltage]":       100,
	"battery:1[voltage]":       100,
	"battery:0[current]":       250,
	"battery:1[current]":       250,
	"engine-ecu[motor:voltage]": 50,
	"engine-ecu[motor:current]": 100,
	"aux-battery[voltage]":     50,
	"cb-battery[cell-voltage]": 50,
	"cb-battery[current]":      100,
	"gps[speed]":               1,
}

// TelemetryFlusher recollects and publishes the current snapshot.
type TelemetryFlusher interface {
	Flush(ctx context.Context, forceFull bool) error
}

// EventFlusher interface for flushing buffered events
type EventFlusher interface {
	FlushBufferedEvents(ctx context.Context)
}

// Monitor watches Redis keys for changes and decides when to trigger a
// telemetry flush. It does not build the payload itself; the flusher recollects
// a fresh snapshot and diffs it.
type Monitor struct {
	client       *ipc.Client
	collector    *Collector
	connMgr      *connection.Manager
	flusher      TelemetryFlusher
	eventFlusher EventFlusher
	ctx          context.Context

	mu       sync.Mutex
	watchers []*ipc.HashWatcher

	priorityDeadlines map[Priority]time.Duration
	priorityArmed     map[Priority]*time.Timer

	lastValues map[string]string
}

// NewMonitor creates a new state monitor
func NewMonitor(client *ipc.Client, collector *Collector, connMgr *connection.Manager) *Monitor {
	return &Monitor{
		client:            client,
		collector:         collector,
		connMgr:           connMgr,
		priorityDeadlines: priorityDeadlines,
		priorityArmed:     make(map[Priority]*time.Timer),
		lastValues:        make(map[string]string),
	}
}

// SetFlusher wires the telemetry flusher used when a deadline fires.
func (m *Monitor) SetFlusher(f TelemetryFlusher) {
	m.flusher = f
}

// SetEventFlusher sets the event flusher for bidirectional flushing
func (m *Monitor) SetEventFlusher(ef EventFlusher) {
	m.eventFlusher = ef
}

// InitializeBaseline seeds the change-detection values from a snapshot, applying
// quantization so the first sub-bucket change does not trigger a spurious flush.
func (m *Monitor) InitializeBaseline(state map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for hash, fields := range state {
		fieldMap, ok := fields.(map[string]any)
		if !ok {
			continue
		}
		for field, value := range fieldMap {
			strVal, ok := value.(string)
			if !ok {
				continue
			}
			fullKey := hash + "[" + field + "]"
			m.lastValues[fullKey] = quantize(fullKey, strVal)
		}
	}
	log.Printf("[Monitor] Initialized baseline with %d field values", len(m.lastValues))
}

// Start begins monitoring Redis for changes
func (m *Monitor) Start(ctx context.Context) {
	m.ctx = ctx
	log.Println("[Monitor] Starting Redis PUBSUB monitoring with HashWatchers...")

	channels := []string{
		"vehicle", "battery:0", "battery:1", "aux-battery", "cb-battery",
		"engine-ecu", "gps", "internet", "modem", "power-manager", "power-mux",
		"keycard", "ble", "ota", "alarm", "dashboard", "system",
	}

	for _, channel := range channels {
		ch := channel
		watcher := m.client.NewHashWatcher(ch)
		watcher.OnAny(func(field, value string) error {
			return m.handleFieldChange(ch, field, value)
		})
		watcher.Start()
		m.watchers = append(m.watchers, watcher)
	}

	log.Printf("[Monitor] Started %d HashWatchers", len(m.watchers))

	<-ctx.Done()

	for _, watcher := range m.watchers {
		watcher.Stop()
	}
}

// handleFieldChange decides whether a field change should schedule a flush.
func (m *Monitor) handleFieldChange(hash, field, value string) error {
	fullKey := hash + "[" + field + "]"

	if noisyFields[fullKey] {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Apply the hop-on cloud-facing remap before dedup so lastValues reflects
	// the cloud-facing value.
	if hash == "vehicle" && field == "state" {
		value = cloudifyVehicleState(value)
	}

	q := quantize(fullKey, value)
	if m.lastValues[fullKey] == q {
		return nil
	}
	m.lastValues[fullKey] = q

	m.armLocked(m.priorityFor(hash, field))
	return nil
}

// armLocked starts a priority's deadline timer if not already running. Deadline
// semantics: the timer is not reset by subsequent changes. Caller holds m.mu.
func (m *Monitor) armLocked(priority Priority) {
	if m.priorityArmed[priority] != nil {
		return
	}
	deadline := m.priorityDeadlines[priority]
	m.priorityArmed[priority] = time.AfterFunc(deadline, func() {
		m.onDeadline(priority)
	})
}

// onDeadline fires when a priority's timer elapses: cancel all armed timers and
// trigger a single flush covering every pending change.
func (m *Monitor) onDeadline(priority Priority) {
	m.mu.Lock()
	m.clearTimersLocked()
	connected := m.connMgr.IsConnected()
	m.mu.Unlock()

	if !connected {
		// Rearm nothing; a reconnect triggers a full flush from main.
		return
	}

	log.Printf("[Monitor] Flush (triggered by %s)", priorityNames[priority])
	m.doFlush()
}

// FlushAllPending triggers an immediate flush (used on reconnect).
func (m *Monitor) FlushAllPending() {
	m.mu.Lock()
	m.clearTimersLocked()
	connected := m.connMgr.IsConnected()
	m.mu.Unlock()

	if !connected {
		return
	}
	m.doFlush()
}

func (m *Monitor) doFlush() {
	if m.flusher != nil {
		if err := m.flusher.Flush(m.ctx, false); err != nil {
			log.Printf("[Monitor] Flush failed: %v", err)
		}
	}
	if m.eventFlusher != nil {
		go m.eventFlusher.FlushBufferedEvents(m.ctx)
	}
}

func (m *Monitor) clearTimersLocked() {
	for prio, timer := range m.priorityArmed {
		if timer != nil {
			timer.Stop()
		}
		delete(m.priorityArmed, prio)
	}
}

// priorityFor determines the flush priority for a field.
func (m *Monitor) priorityFor(hash, field string) Priority {
	fullKey := hash + "[" + field + "]"
	if prio, ok := fieldPriorities[fullKey]; ok {
		return prio
	}
	if prio, ok := hashPriorities[hash]; ok {
		return prio
	}
	return Medium
}

// quantize returns a canonical dedup key for a field value: analog fields are
// snapped to their bucket, everything else passes through unchanged.
func quantize(fullKey, value string) string {
	bucket, ok := quantizationBuckets[fullKey]
	if !ok || bucket == 0 {
		return value
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return value
	}
	snapped := float64(int64(f/bucket+0.5)) * bucket
	return strconv.FormatFloat(snapped, 'f', -1, 64)
}
