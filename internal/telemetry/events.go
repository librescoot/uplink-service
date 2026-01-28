package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	ipc "github.com/librescoot/redis-ipc"

	"github.com/librescoot/uplink-service/internal/connection"
)

// TelemetryMonitor interface for flushing pending changes
type TelemetryMonitor interface {
	FlushAllPending()
}

// EventDetector monitors for critical conditions and sends event messages
type EventDetector struct {
	client        *ipc.Client
	connMgr       *connection.Manager
	monitor       TelemetryMonitor
	bufferPath    string
	maxRetries    int
	faultConsumer *ipc.StreamConsumer

	watchers  []*ipc.HashWatcher
	lastState map[string]string
}

// NewEventDetector creates a new event detector
func NewEventDetector(client *ipc.Client, connMgr *connection.Manager, monitor TelemetryMonitor, bufferPath string, maxRetries int) *EventDetector {
	return &EventDetector{
		client:     client,
		connMgr:    connMgr,
		monitor:    monitor,
		bufferPath: bufferPath,
		maxRetries: maxRetries,
		lastState:  make(map[string]string),
	}
}

// InitializeBaseline sets the initial state from a state snapshot
func (e *EventDetector) InitializeBaseline(state map[string]any) {
	for hash, fields := range state {
		if fieldMap, ok := fields.(map[string]any); ok {
			for field, value := range fieldMap {
				stateKey := hash + ":" + field
				if strVal, ok := value.(string); ok {
					e.lastState[stateKey] = strVal
				}
			}
		}
	}
	log.Printf("[EventDetector] Initialized baseline with %d field values", len(e.lastState))
}

// Start begins monitoring for events
func (e *EventDetector) Start(ctx context.Context) {
	log.Println("[EventDetector] Starting with HashWatchers...")

	// Battery watchers - monitor charge and present fields
	for _, battery := range []string{"battery:0", "battery:1"} {
		w := e.client.NewHashWatcher(battery)
		w.OnField("charge", e.makeBatteryChargeHandler(battery))
		w.OnField("present", e.makeBatteryPresentHandler(battery))
		w.OnField("temperature", e.makeTemperatureHandler(battery, "temperature"))
		w.Start()
		e.watchers = append(e.watchers, w)
	}

	// Power manager watcher
	pmWatcher := e.client.NewHashWatcher("power-manager")
	pmWatcher.OnField("state", e.handlePowerState)
	pmWatcher.OnField("nrf-reset-reason", e.handleNrfReset)
	pmWatcher.Start()
	e.watchers = append(e.watchers, pmWatcher)

	// Internet watcher
	internetWatcher := e.client.NewHashWatcher("internet")
	internetWatcher.OnField("status", e.handleConnectivityStatus)
	internetWatcher.Start()
	e.watchers = append(e.watchers, internetWatcher)

	// Vehicle watcher
	vehicleWatcher := e.client.NewHashWatcher("vehicle")
	vehicleWatcher.OnField("handlebar:lock-sensor", e.makeHandlebarLockHandler())
	vehicleWatcher.OnField("seatbox:lock", e.makeSeatboxLockHandler())
	vehicleWatcher.Start()
	e.watchers = append(e.watchers, vehicleWatcher)

	// GPS watcher
	gpsWatcher := e.client.NewHashWatcher("gps")
	gpsWatcher.OnField("state", e.handleGPSState)
	gpsWatcher.Start()
	e.watchers = append(e.watchers, gpsWatcher)

	// Engine ECU watcher
	ecuWatcher := e.client.NewHashWatcher("engine-ecu")
	ecuWatcher.OnField("temperature", e.makeTemperatureHandler("engine-ecu", "temperature"))
	ecuWatcher.Start()
	e.watchers = append(e.watchers, ecuWatcher)

	// CB Battery watcher - monitor control board battery
	cbBatteryWatcher := e.client.NewHashWatcher("cb-battery")
	cbBatteryWatcher.OnField("charge", e.makeCBBatteryChargeHandler())
	cbBatteryWatcher.Start()
	e.watchers = append(e.watchers, cbBatteryWatcher)

	log.Printf("[EventDetector] Started %d HashWatchers", len(e.watchers))

	// Fault stream consumer - monitor fault events
	e.faultConsumer = e.client.NewStreamConsumer("events:faults")
	e.faultConsumer.Handle(e.handleFault)
	e.faultConsumer.Start("$") // Start from new messages only
	log.Println("[EventDetector] Started fault stream consumer")

	// Block until context is done
	<-ctx.Done()

	// Stop fault consumer
	if e.faultConsumer != nil {
		e.faultConsumer.Stop()
	}

	// Stop all watchers
	for _, watcher := range e.watchers {
		watcher.Stop()
	}
}

// makeBatteryChargeHandler creates a handler for battery charge field
func (e *EventDetector) makeBatteryChargeHandler(battery string) func(string) error {
	return func(value string) error {
		stateKey := battery + ":charge"
		presentKey := battery + ":present"
		chargeInt := parseInt(value)

		// Only emit battery_critical if battery is present
		present := e.lastState[presentKey]
		if present == "true" && chargeInt <= 10 && e.lastState[stateKey] != value {
			e.sendEvent(context.Background(), "battery_critical", map[string]any{
				"battery": battery,
				"charge":  chargeInt,
			})
		}

		e.lastState[stateKey] = value
		return nil
	}
}

// makeBatteryPresentHandler creates a handler for battery present field
func (e *EventDetector) makeBatteryPresentHandler(battery string) func(string) error {
	return func(value string) error {
		stateKey := battery + ":present"
		e.lastState[stateKey] = value
		return nil
	}
}

// makeCBBatteryChargeHandler creates a handler for CB battery charge field
func (e *EventDetector) makeCBBatteryChargeHandler() func(string) error {
	return func(value string) error {
		stateKey := "cb-battery:charge"
		chargeInt := parseInt(value)

		// Emit event if charge is critical and value changed
		if chargeInt <= 10 && e.lastState[stateKey] != value {
			e.sendEvent(context.Background(), "cb_battery_critical", map[string]any{
				"battery": "cb-battery",
				"charge":  chargeInt,
			})
		}

		e.lastState[stateKey] = value
		return nil
	}
}

// handlePowerState handles power manager state changes
func (e *EventDetector) handlePowerState(value string) error {
	stateKey := "power:state"

	if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
		e.sendEvent(context.Background(), "power_state_change", map[string]any{
			"from": e.lastState[stateKey],
			"to":   value,
		})
	}

	e.lastState[stateKey] = value
	return nil
}

// handleNrfReset handles NRF wireless module reset events
func (e *EventDetector) handleNrfReset(value string) error {
	stateKey := "power-manager:nrf-reset-reason"

	// Only emit event if reason changed
	if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
		reasonInt := parseInt(value)

		// Read reset count from Redis for context
		countStr, _ := e.client.HGet("power-manager", "nrf-reset-count")
		countInt := parseInt(countStr)

		e.sendEvent(context.Background(), "nrf_reset", map[string]any{
			"reason": fmt.Sprintf("0x%x", reasonInt),
			"count":  countInt,
		})
	}

	e.lastState[stateKey] = value
	return nil
}

// handleConnectivityStatus handles internet status changes
func (e *EventDetector) handleConnectivityStatus(value string) error {
	stateKey := "internet:status"

	if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
		eventType := "connectivity_lost"
		if value == "connected" {
			eventType = "connectivity_regained"
		}

		e.sendEvent(context.Background(), eventType, map[string]any{
			"status": value,
		})
	}

	e.lastState[stateKey] = value
	return nil
}

// makeHandlebarLockHandler creates a handler for handlebar lock sensor
func (e *EventDetector) makeHandlebarLockHandler() func(string) error {
	return func(value string) error {
		stateKey := "vehicle:handlebar"

		if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
			e.sendEvent(context.Background(), "lock_state_change", map[string]any{
				"lock":  "handlebar",
				"state": value,
			})
		}

		e.lastState[stateKey] = value
		return nil
	}
}

// makeSeatboxLockHandler creates a handler for seatbox lock
func (e *EventDetector) makeSeatboxLockHandler() func(string) error {
	return func(value string) error {
		stateKey := "vehicle:seatbox"

		if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
			e.sendEvent(context.Background(), "lock_state_change", map[string]any{
				"lock":  "seatbox",
				"state": value,
			})
		}

		e.lastState[stateKey] = value
		return nil
	}
}

// handleGPSState handles GPS state changes
func (e *EventDetector) handleGPSState(value string) error {
	stateKey := "gps:state"

	if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
		eventType := "gps_fix_lost"
		if value == "fix-3d" || value == "fix-2d" {
			eventType = "gps_fix_regained"
		}

		e.sendEvent(context.Background(), eventType, map[string]any{
			"state": value,
		})
	}

	e.lastState[stateKey] = value
	return nil
}

// makeTemperatureHandler creates a handler for temperature warnings
func (e *EventDetector) makeTemperatureHandler(component, field string) func(string) error {
	return func(value string) error {
		stateKey := component + ":" + field
		tempInt := parseInt(value)

		threshold := 80
		if component == "battery:0" || component == "battery:1" {
			threshold = 60
		}

		if tempInt > threshold && e.lastState[stateKey] != value {
			e.sendEvent(context.Background(), "temperature_warning", map[string]any{
				"component":   component,
				"temperature": tempInt,
			})
		}

		e.lastState[stateKey] = value
		return nil
	}
}

// handleFault processes fault events from the events:faults stream
func (e *EventDetector) handleFault(id string, values map[string]string) error {
	// Require both group and code fields
	group, hasGroup := values["group"]
	codeStr, hasCode := values["code"]

	if !hasGroup || !hasCode {
		log.Printf("[EventDetector] Ignoring fault entry %s: missing required fields", id)
		return nil
	}

	// Parse code
	code := parseInt(codeStr)

	// Build event data
	eventData := map[string]any{
		"group": group,
		"code":  code,
	}

	// Add description if present
	if desc, hasDesc := values["description"]; hasDesc {
		eventData["description"] = desc
	}

	e.sendEvent(context.Background(), "fault", eventData)
	return nil
}

// formatEventContext formats event data as (key=val, key=val) for logging
func formatEventContext(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}

	// Build context string with sorted keys for consistency
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		v := data[k]
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}

	return fmt.Sprintf("(%s)", strings.Join(parts, ", "))
}

// sendEvent sends an event, buffering if not connected
func (e *EventDetector) sendEvent(ctx context.Context, eventType string, data map[string]any) {
	log.Printf("[EventDetector] Event: %s %s", eventType, formatEventContext(data))

	event := map[string]any{
		"event":     eventType,
		"data":      data,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	// Try to send immediately
	if e.connMgr.IsConnected() {
		if err := e.connMgr.SendEvent(eventType, data); err != nil {
			log.Printf("[EventDetector] Failed to send event, buffering: %v", err)
			e.bufferEvent(event)
		} else {
			// Successfully sent event - flush buffered events and pending telemetry
			go e.flushBufferedEvents(ctx)
			if e.monitor != nil {
				go e.monitor.FlushAllPending()
			}
		}
	} else {
		log.Println("[EventDetector] Not connected, buffering event")
		e.bufferEvent(event)
	}
}

// bufferEvent writes an event to persistent storage
func (e *EventDetector) bufferEvent(event map[string]any) {
	// Initialize retry count if not present
	if _, ok := event["retries"]; !ok {
		event["retries"] = 0
	}

	// Ensure directory exists
	dir := filepath.Dir(e.bufferPath)
	os.MkdirAll(dir, 0755)

	// Open file in append mode
	f, err := os.OpenFile(e.bufferPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[EventDetector] Failed to open buffer file: %v", err)
		return
	}
	defer f.Close()

	// Write JSON line
	data, _ := json.Marshal(event)
	f.Write(data)
	f.Write([]byte("\n"))

	log.Printf("[EventDetector] Buffered event to %s", e.bufferPath)
}

// FlushBufferedEvents attempts to send all buffered events with retry logic
func (e *EventDetector) FlushBufferedEvents(ctx context.Context) {
	e.flushBufferedEvents(ctx)
}

// flushBufferedEvents sends all buffered events with retry logic
func (e *EventDetector) flushBufferedEvents(ctx context.Context) {
	if !e.connMgr.IsConnected() {
		return
	}

	// Check if buffer file exists
	if _, err := os.Stat(e.bufferPath); os.IsNotExist(err) {
		return
	}

	log.Println("[EventDetector] Flushing buffered events...")

	// Read buffer file
	data, err := os.ReadFile(e.bufferPath)
	if err != nil {
		log.Printf("[EventDetector] Failed to read buffer: %v", err)
		return
	}

	// Parse events
	lines := splitLines(string(data))
	var failedEvents []map[string]any
	successCount := 0
	discardedCount := 0

	for _, line := range lines {
		if line == "" {
			continue
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			log.Printf("[EventDetector] Failed to parse buffered event, discarding: %v", err)
			discardedCount++
			continue
		}

		// Get retry count
		retries := 0
		if r, ok := event["retries"].(float64); ok {
			retries = int(r)
		}

		// Check if exceeded max retries
		if retries >= e.maxRetries {
			eventType, _ := event["event"].(string)
			log.Printf("[EventDetector] Event %s exceeded max retries (%d), discarding", eventType, e.maxRetries)
			discardedCount++
			continue
		}

		// Try to send
		eventType, _ := event["event"].(string)
		eventData, _ := event["data"].(map[string]any)

		if err := e.connMgr.SendEvent(eventType, eventData); err != nil {
			log.Printf("[EventDetector] Failed to send buffered event %s (retry %d/%d): %v",
				eventType, retries+1, e.maxRetries, err)
			// Increment retry count and save for later
			event["retries"] = retries + 1
			failedEvents = append(failedEvents, event)
			// Exponential backoff delay
			backoff := time.Duration(1<<uint(retries)) * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}

		successCount++
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}

	log.Printf("[EventDetector] Flushed %d events, %d failed (will retry), %d discarded",
		successCount, len(failedEvents), discardedCount)

	// Rewrite buffer with only failed events
	if len(failedEvents) > 0 {
		f, err := os.OpenFile(e.bufferPath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("[EventDetector] Failed to rewrite buffer: %v", err)
			return
		}
		defer f.Close()

		for _, event := range failedEvents {
			data, _ := json.Marshal(event)
			f.Write(data)
			f.Write([]byte("\n"))
		}
		log.Printf("[EventDetector] Rewrote buffer with %d failed events", len(failedEvents))
	} else {
		// All sent successfully, remove buffer
		os.Remove(e.bufferPath)
	}
}

// splitLines splits string by newlines
func splitLines(s string) []string {
	lines := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func parseInt(s string) int {
	val, _ := strconv.Atoi(s)
	return val
}
