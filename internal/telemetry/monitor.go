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

var hashPriorities = map[string]Priority{
	"gps":       Quick,
	"battery:0": Quick,
	"battery:1": Quick,
}

// These fields remain in snapshots but cannot trigger transmission on their own.
var noisyFields = map[string]bool{
	"gps[timestamp]":           true,
	"gps[latitude]":            true,
	"gps[longitude]":           true,
	"gps[altitude]":            true,
	"gps[course]":              true,
	"internet[signal-quality]": true,
}

// Ignore sensor changes below these reporting thresholds (native Redis units:
// millivolts, milliamps, and GPS speed) when deciding to schedule a flush.
var quantizationBuckets = map[string]float64{
	"battery:0[voltage]":        100,
	"battery:1[voltage]":        100,
	"battery:0[current]":        250,
	"battery:1[current]":        250,
	"engine-ecu[motor:voltage]": 50,
	"engine-ecu[motor:current]": 100,
	"aux-battery[voltage]":      50,
	"cb-battery[cell-voltage]":  50,
	"cb-battery[current]":       100,
	"gps[speed]":                1,
}

type TelemetryFlusher interface {
	Flush(ctx context.Context, forceFull bool) error
}

type EventFlusher interface {
	FlushBufferedEvents(ctx context.Context)
}

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

func (m *Monitor) SetFlusher(f TelemetryFlusher) {
	m.flusher = f
}

func (m *Monitor) SetEventFlusher(ef EventFlusher) {
	m.eventFlusher = ef
}

// InitializeBaseline quantizes first, avoiding a spurious flush for the first
// sub-threshold measurement change.
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
		if err := watcher.Start(); err != nil {
			log.Printf("[Monitor] Failed to start watcher for %s: %v", ch, err)
		}
		m.watchers = append(m.watchers, watcher)
	}

	log.Printf("[Monitor] Started %d HashWatchers", len(m.watchers))

	<-ctx.Done()

	for _, watcher := range m.watchers {
		_ = watcher.Stop()
	}
}

func (m *Monitor) handleFieldChange(hash, field, value string) error {
	fullKey := hash + "[" + field + "]"

	if noisyFields[fullKey] {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

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

// armLocked never resets a deadline; later changes join its pending flush.
func (m *Monitor) armLocked(priority Priority) {
	if m.priorityArmed[priority] != nil {
		return
	}
	deadline := m.priorityDeadlines[priority]
	m.priorityArmed[priority] = time.AfterFunc(deadline, func() {
		m.onDeadline(priority)
	})
}

// onDeadline collapses all pending priorities into one fresh telemetry flush.
func (m *Monitor) onDeadline(priority Priority) {
	m.mu.Lock()
	m.clearTimersLocked()
	connected := m.connMgr.IsConnected()
	m.mu.Unlock()

	if !connected {

		return
	}

	log.Printf("[Monitor] Flush (triggered by %s)", priorityNames[priority])
	m.doFlush()
}

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
