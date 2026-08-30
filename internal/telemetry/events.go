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

type TelemetryMonitor interface {
	FlushAllPending()
}

type EventDetector struct {
	client        *ipc.Client
	connMgr       *connection.Manager
	monitor       TelemetryMonitor
	bufferPath    string
	maxRetries    int
	faultConsumer *ipc.StreamConsumer
	ctx           context.Context

	watchers  []*ipc.HashWatcher
	lastState map[string]string
}

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

func (e *EventDetector) Start(ctx context.Context) {
	e.ctx = ctx
	log.Println("[EventDetector] Starting with HashWatchers...")

	for _, battery := range []string{"battery:0", "battery:1"} {
		w := e.client.NewHashWatcher(battery)
		w.OnField("charge", e.makeBatteryChargeHandler(battery))
		w.OnField("present", e.makeBatteryPresentHandler(battery))
		w.OnField("temperature", e.makeTemperatureHandler(battery, "temperature"))
		if err := w.Start(); err != nil {
			log.Printf("[EventDetector] Failed to start watcher for %s: %v", battery, err)
		}
		e.watchers = append(e.watchers, w)
	}

	pmWatcher := e.client.NewHashWatcher("power-manager")
	pmWatcher.OnField("state", e.handlePowerState)
	pmWatcher.OnField("nrf-reset-reason", e.handleNrfReset)
	if err := pmWatcher.Start(); err != nil {
		log.Printf("[EventDetector] Failed to start power manager watcher: %v", err)
	}
	e.watchers = append(e.watchers, pmWatcher)

	internetWatcher := e.client.NewHashWatcher("internet")
	internetWatcher.OnField("status", e.handleConnectivityStatus)
	if err := internetWatcher.Start(); err != nil {
		log.Printf("[EventDetector] Failed to start internet watcher: %v", err)
	}
	e.watchers = append(e.watchers, internetWatcher)

	vehicleWatcher := e.client.NewHashWatcher("vehicle")
	vehicleWatcher.OnField("handlebar:lock-sensor", e.makeHandlebarLockHandler())
	vehicleWatcher.OnField("seatbox:lock", e.makeSeatboxLockHandler())
	if err := vehicleWatcher.Start(); err != nil {
		log.Printf("[EventDetector] Failed to start vehicle watcher: %v", err)
	}
	e.watchers = append(e.watchers, vehicleWatcher)

	gpsWatcher := e.client.NewHashWatcher("gps")
	gpsWatcher.OnField("state", e.handleGPSState)
	if err := gpsWatcher.Start(); err != nil {
		log.Printf("[EventDetector] Failed to start GPS watcher: %v", err)
	}
	e.watchers = append(e.watchers, gpsWatcher)

	ecuWatcher := e.client.NewHashWatcher("engine-ecu")
	ecuWatcher.OnField("temperature", e.makeTemperatureHandler("engine-ecu", "temperature"))
	if err := ecuWatcher.Start(); err != nil {
		log.Printf("[EventDetector] Failed to start engine ECU watcher: %v", err)
	}
	e.watchers = append(e.watchers, ecuWatcher)

	cbBatteryWatcher := e.client.NewHashWatcher("cb-battery")
	cbBatteryWatcher.OnField("charge", e.makeCBBatteryChargeHandler())
	if err := cbBatteryWatcher.Start(); err != nil {
		log.Printf("[EventDetector] Failed to start CB battery watcher: %v", err)
	}
	e.watchers = append(e.watchers, cbBatteryWatcher)

	otaWatcher := e.client.NewHashWatcher("ota")
	otaWatcher.OnField("status", e.handleOTAStatus)
	if err := otaWatcher.Start(); err != nil {
		log.Printf("[EventDetector] Failed to start OTA watcher: %v", err)
	}
	e.watchers = append(e.watchers, otaWatcher)

	alarmWatcher := e.client.NewHashWatcher("alarm")
	alarmWatcher.OnField("alarm-active", e.handleAlarmActive)
	alarmWatcher.OnField("status", e.handleAlarmStatus)
	if err := alarmWatcher.Start(); err != nil {
		log.Printf("[EventDetector] Failed to start alarm watcher: %v", err)
	}
	e.watchers = append(e.watchers, alarmWatcher)

	log.Printf("[EventDetector] Started %d HashWatchers", len(e.watchers))

	e.faultConsumer = e.client.NewStreamConsumer("events:faults")
	e.faultConsumer.Handle(e.handleFault)
	// '$' starts at new Redis Stream entries; historical faults are not replayed.
	if err := e.faultConsumer.Start("$"); err != nil {
		log.Printf("[EventDetector] Failed to start fault stream consumer: %v", err)
	}
	log.Println("[EventDetector] Started fault stream consumer")

	<-ctx.Done()

	if e.faultConsumer != nil {
		e.faultConsumer.Stop()
	}

	for _, watcher := range e.watchers {
		_ = watcher.Stop()
	}
}

func (e *EventDetector) makeBatteryChargeHandler(battery string) func(string) error {
	return func(value string) error {
		stateKey := battery + ":charge"
		presentKey := battery + ":present"
		chargeInt := parseInt(value)

		present := e.lastState[presentKey]
		if present == "true" && chargeInt <= 10 && e.lastState[stateKey] != value {
			e.sendEvent(e.ctx, "battery_critical", map[string]any{
				"battery": battery,
				"charge":  chargeInt,
			})
		}

		e.lastState[stateKey] = value
		return nil
	}
}

func (e *EventDetector) makeBatteryPresentHandler(battery string) func(string) error {
	return func(value string) error {
		stateKey := battery + ":present"
		e.lastState[stateKey] = value
		return nil
	}
}

func (e *EventDetector) makeCBBatteryChargeHandler() func(string) error {
	return func(value string) error {
		stateKey := "cb-battery:charge"
		chargeInt := parseInt(value)

		if chargeInt <= 10 && e.lastState[stateKey] != value {
			e.sendEvent(e.ctx, "cb_battery_critical", map[string]any{
				"battery": "cb-battery",
				"charge":  chargeInt,
			})
		}

		e.lastState[stateKey] = value
		return nil
	}
}

func (e *EventDetector) handlePowerState(value string) error {
	stateKey := "power:state"

	if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
		e.sendEvent(e.ctx, "power_state_change", map[string]any{
			"from": e.lastState[stateKey],
			"to":   value,
		})
	}

	e.lastState[stateKey] = value
	return nil
}

func (e *EventDetector) handleNrfReset(value string) error {
	stateKey := "power-manager:nrf-reset-reason"

	if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
		reasonInt := parseInt(value)

		countStr, _ := e.client.HGet("power-manager", "nrf-reset-count")
		countInt := parseInt(countStr)

		e.sendEvent(e.ctx, "nrf_reset", map[string]any{
			"reason": fmt.Sprintf("0x%x", reasonInt),
			"count":  countInt,
		})
	}

	e.lastState[stateKey] = value
	return nil
}

func (e *EventDetector) handleConnectivityStatus(value string) error {
	stateKey := "internet:status"

	if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
		eventType := "connectivity_lost"
		if value == "connected" {
			eventType = "connectivity_regained"
		}

		e.sendEvent(e.ctx, eventType, map[string]any{
			"status": value,
		})
	}

	e.lastState[stateKey] = value
	return nil
}

func (e *EventDetector) makeHandlebarLockHandler() func(string) error {
	return func(value string) error {
		stateKey := "vehicle:handlebar"

		if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
			e.sendEvent(e.ctx, "lock_state_change", map[string]any{
				"lock":  "handlebar",
				"state": value,
			})
		}

		e.lastState[stateKey] = value
		return nil
	}
}

func (e *EventDetector) makeSeatboxLockHandler() func(string) error {
	return func(value string) error {
		stateKey := "vehicle:seatbox"

		if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
			e.sendEvent(e.ctx, "lock_state_change", map[string]any{
				"lock":  "seatbox",
				"state": value,
			})
		}

		e.lastState[stateKey] = value
		return nil
	}
}

func (e *EventDetector) handleGPSState(value string) error {
	stateKey := "gps:state"

	if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
		eventType := "gps_fix_lost"
		if value == "fix-3d" || value == "fix-2d" {
			eventType = "gps_fix_regained"
		}

		e.sendEvent(e.ctx, eventType, map[string]any{
			"state": value,
		})
	}

	e.lastState[stateKey] = value
	return nil
}

func (e *EventDetector) handleOTAStatus(value string) error {
	stateKey := "ota:status"

	if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
		e.sendEvent(e.ctx, "ota_status_change", map[string]any{
			"from": e.lastState[stateKey],
			"to":   value,
		})
	}

	e.lastState[stateKey] = value
	return nil
}

func (e *EventDetector) handleAlarmActive(value string) error {
	stateKey := "alarm:alarm-active"

	if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
		eventType := "alarm_cleared"
		if value == "true" {
			eventType = "alarm_triggered"
		}
		e.sendEvent(e.ctx, eventType, map[string]any{
			"active": value,
		})
	}

	e.lastState[stateKey] = value
	return nil
}

func (e *EventDetector) handleAlarmStatus(value string) error {
	stateKey := "alarm:status"

	if e.lastState[stateKey] != "" && e.lastState[stateKey] != value {
		e.sendEvent(e.ctx, "alarm_state_change", map[string]any{
			"from": e.lastState[stateKey],
			"to":   value,
		})
	}

	e.lastState[stateKey] = value
	return nil
}

func (e *EventDetector) makeTemperatureHandler(component, field string) func(string) error {
	return func(value string) error {
		stateKey := component + ":" + field
		tempInt := parseInt(value)

		threshold := 80
		if component == "battery:0" || component == "battery:1" {
			threshold = 60
		}

		if tempInt > threshold && e.lastState[stateKey] != value {
			e.sendEvent(e.ctx, "temperature_warning", map[string]any{
				"component":   component,
				"temperature": tempInt,
			})
		}

		e.lastState[stateKey] = value
		return nil
	}
}

func (e *EventDetector) handleFault(id string, values map[string]string) error {

	group, hasGroup := values["group"]
	codeStr, hasCode := values["code"]

	if !hasGroup || !hasCode {
		log.Printf("[EventDetector] Ignoring fault entry %s: missing required fields", id)
		return nil
	}

	code := parseInt(codeStr)

	eventData := map[string]any{
		"group": group,
		"code":  code,
	}

	if desc, hasDesc := values["description"]; hasDesc {
		eventData["description"] = desc
	}

	e.sendEvent(e.ctx, "fault", eventData)
	return nil
}

func formatEventContext(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}

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

func (e *EventDetector) sendEvent(ctx context.Context, eventType string, data map[string]any) {
	log.Printf("[EventDetector] Event: %s %s", eventType, formatEventContext(data))

	event := map[string]any{
		"event":     eventType,
		"data":      data,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	if e.connMgr.IsConnected() {
		if err := e.connMgr.SendEvent(eventType, data); err != nil {
			log.Printf("[EventDetector] Failed to send event, buffering: %v", err)
			e.bufferEvent(event)
		} else {

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

func (e *EventDetector) bufferEvent(event map[string]any) {

	if _, ok := event["retries"]; !ok {
		event["retries"] = 0
	}

	dir := filepath.Dir(e.bufferPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[EventDetector] Failed to create buffer directory: %v", err)
		return
	}

	f, err := os.OpenFile(e.bufferPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[EventDetector] Failed to open buffer file: %v", err)
		return
	}
	defer f.Close()

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("[EventDetector] Failed to marshal event: %v", err)
		return
	}
	if _, err := f.Write(data); err != nil {
		log.Printf("[EventDetector] Failed to write event to buffer: %v", err)
		return
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		log.Printf("[EventDetector] Failed to write newline to buffer: %v", err)
		return
	}

	log.Printf("[EventDetector] Buffered event to %s", e.bufferPath)
}

func (e *EventDetector) FlushBufferedEvents(ctx context.Context) {
	e.flushBufferedEvents(ctx)
}

func (e *EventDetector) flushBufferedEvents(ctx context.Context) {
	if !e.connMgr.IsConnected() {
		return
	}

	if _, err := os.Stat(e.bufferPath); os.IsNotExist(err) {
		return
	}

	log.Println("[EventDetector] Flushing buffered events...")

	data, err := os.ReadFile(e.bufferPath)
	if err != nil {
		log.Printf("[EventDetector] Failed to read buffer: %v", err)
		return
	}

	lines := splitLines(string(data))
	var failedEvents []map[string]any
	successCount := 0
	discardedCount := 0

	for i, line := range lines {
		if line == "" {
			continue
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			log.Printf("[EventDetector] Failed to parse buffered event, discarding: %v", err)
			discardedCount++
			continue
		}

		retries := 0
		if r, ok := event["retries"].(float64); ok {
			retries = int(r)
		}

		if retries >= e.maxRetries {
			eventType, _ := event["event"].(string)
			log.Printf("[EventDetector] Event %s exceeded max retries (%d), discarding", eventType, e.maxRetries)
			discardedCount++
			continue
		}

		eventType, _ := event["event"].(string)
		eventData, _ := event["data"].(map[string]any)

		if err := e.connMgr.SendEvent(eventType, eventData); err != nil {
			log.Printf("[EventDetector] Failed to send buffered event %s (retry %d/%d): %v",
				eventType, retries+1, e.maxRetries, err)
			event["retries"] = retries + 1
			failedEvents = append(failedEvents, event)

			for _, remaining := range lines[i+1:] {
				if remaining == "" {
					continue
				}
				var ev map[string]any
				if err := json.Unmarshal([]byte(remaining), &ev); err == nil {
					failedEvents = append(failedEvents, ev)
				}
			}
			break
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

	if len(failedEvents) > 0 {
		f, err := os.OpenFile(e.bufferPath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("[EventDetector] Failed to rewrite buffer: %v", err)
			return
		}
		defer f.Close()

		for _, event := range failedEvents {
			data, _ := json.Marshal(event)
			if _, err := f.Write(data); err != nil {
				log.Printf("[EventDetector] Failed to write event to buffer: %v", err)
				break
			}
			if _, err := f.Write([]byte("\n")); err != nil {
				log.Printf("[EventDetector] Failed to write newline to buffer: %v", err)
				break
			}
		}
		log.Printf("[EventDetector] Rewrote buffer with %d failed events", len(failedEvents))
	} else {

		if err := os.Remove(e.bufferPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[EventDetector] Failed to remove buffer file: %v", err)
		}
	}
}

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
