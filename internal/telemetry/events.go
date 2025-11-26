package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/librescoot/uplink-service/internal/connection"
)

// EventDetector monitors for critical conditions and sends event messages
type EventDetector struct {
	redisClient *redis.Client
	connMgr     *connection.Manager
	bufferPath  string
	maxRetries  int

	lastState map[string]string
}

// NewEventDetector creates a new event detector
func NewEventDetector(redisClient *redis.Client, connMgr *connection.Manager, bufferPath string, maxRetries int) *EventDetector {
	return &EventDetector{
		redisClient: redisClient,
		connMgr:     connMgr,
		bufferPath:  bufferPath,
		maxRetries:  maxRetries,
		lastState:   make(map[string]string),
	}
}

// Start begins monitoring for events
func (e *EventDetector) Start(ctx context.Context) {
	log.Println("[EventDetector] Starting...")

	// First, flush any buffered events
	go e.flushBufferedEvents(ctx)

	// Subscribe to notification channels
	channels := []string{
		"vehicle", "battery:0", "battery:1", "power-manager",
		"internet", "gps", "engine-ecu",
	}

	pubsub := e.redisClient.Subscribe(ctx, channels...)
	defer pubsub.Close()

	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			// Channel notification
			e.checkForEvents(ctx, msg.Channel)
		}
	}
}

// checkForEvents examines state changes for event-worthy conditions
func (e *EventDetector) checkForEvents(ctx context.Context, channel string) {
	// Channel name is the key name
	key := channel

	switch key {
	case "battery:0", "battery:1":
		e.checkBatteryCritical(ctx, key)
	case "power-manager":
		e.checkPowerStateChange(ctx)
	case "internet":
		e.checkConnectivityChange(ctx)
	case "vehicle":
		e.checkLockStateChange(ctx)
	case "gps":
		e.checkGPSStateChange(ctx)
	case "engine-ecu":
		e.checkTemperatureWarning(ctx, key)
	}
}

// checkBatteryCritical detects low battery condition
func (e *EventDetector) checkBatteryCritical(ctx context.Context, key string) {
	charge, _ := e.redisClient.HGet(ctx, key, "charge").Result()
	present, _ := e.redisClient.HGet(ctx, key, "present").Result()

	if present != "true" {
		return
	}

	chargeInt := parseInt(charge)
	if chargeInt <= 10 && e.lastState[key+":charge"] != charge {
		e.sendEvent(ctx, "battery_critical", map[string]interface{}{
			"battery": key,
			"charge":  chargeInt,
		})
	}

	e.lastState[key+":charge"] = charge
}

// checkPowerStateChange detects power state transitions
func (e *EventDetector) checkPowerStateChange(ctx context.Context) {
	state, _ := e.redisClient.HGet(ctx, "power-manager", "state").Result()

	if e.lastState["power:state"] != "" && e.lastState["power:state"] != state {
		e.sendEvent(ctx, "power_state_change", map[string]interface{}{
			"from": e.lastState["power:state"],
			"to":   state,
		})
	}

	e.lastState["power:state"] = state
}

// checkConnectivityChange detects connectivity state transitions
func (e *EventDetector) checkConnectivityChange(ctx context.Context) {
	status, _ := e.redisClient.HGet(ctx, "internet", "status").Result()

	if e.lastState["internet:status"] != "" && e.lastState["internet:status"] != status {
		eventType := "connectivity_lost"
		if status == "connected" {
			eventType = "connectivity_regained"
		}

		e.sendEvent(ctx, eventType, map[string]interface{}{
			"status": status,
		})
	}

	e.lastState["internet:status"] = status
}

// checkLockStateChange detects lock state changes
func (e *EventDetector) checkLockStateChange(ctx context.Context) {
	handlebar, _ := e.redisClient.HGet(ctx, "vehicle", "handlebar:lock-sensor").Result()
	seatbox, _ := e.redisClient.HGet(ctx, "vehicle", "seatbox:lock").Result()

	if e.lastState["vehicle:handlebar"] != "" && e.lastState["vehicle:handlebar"] != handlebar {
		e.sendEvent(ctx, "lock_state_change", map[string]interface{}{
			"lock":  "handlebar",
			"state": handlebar,
		})
	}

	if e.lastState["vehicle:seatbox"] != "" && e.lastState["vehicle:seatbox"] != seatbox {
		e.sendEvent(ctx, "lock_state_change", map[string]interface{}{
			"lock":  "seatbox",
			"state": seatbox,
		})
	}

	e.lastState["vehicle:handlebar"] = handlebar
	e.lastState["vehicle:seatbox"] = seatbox
}

// checkGPSStateChange detects GPS fix state changes
func (e *EventDetector) checkGPSStateChange(ctx context.Context) {
	state, _ := e.redisClient.HGet(ctx, "gps", "state").Result()

	if e.lastState["gps:state"] != "" && e.lastState["gps:state"] != state {
		eventType := "gps_fix_lost"
		if state == "fix-3d" || state == "fix-2d" {
			eventType = "gps_fix_regained"
		}

		e.sendEvent(ctx, eventType, map[string]interface{}{
			"state": state,
		})
	}

	e.lastState["gps:state"] = state
}

// checkTemperatureWarning detects overheating conditions
func (e *EventDetector) checkTemperatureWarning(ctx context.Context, key string) {
	temp, _ := e.redisClient.HGet(ctx, key, "temperature").Result()
	tempInt := parseInt(temp)

	// Warn if temperature > 80°C
	if tempInt > 80 && e.lastState[key+":temp"] != temp {
		e.sendEvent(ctx, "temperature_warning", map[string]interface{}{
			"component":   key,
			"temperature": tempInt,
		})
	}

	// Also check battery temperatures
	if key == "battery:0" || key == "battery:1" {
		for i := 0; i < 4; i++ {
			tempKey := fmt.Sprintf("temperature:%d", i)
			temp, _ := e.redisClient.HGet(ctx, key, tempKey).Result()
			tempInt := parseInt(temp)

			if tempInt > 60 {
				e.sendEvent(ctx, "temperature_warning", map[string]interface{}{
					"component":   key,
					"sensor":      i,
					"temperature": tempInt,
				})
			}
		}
	}

	e.lastState[key+":temp"] = temp
}

// sendEvent sends an event, buffering if not connected
func (e *EventDetector) sendEvent(ctx context.Context, eventType string, data map[string]interface{}) {
	log.Printf("[EventDetector] EVENT: %s", eventType)

	event := map[string]interface{}{
		"event":     eventType,
		"data":      data,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	// Try to send immediately
	if e.connMgr.IsConnected() {
		if err := e.connMgr.SendEvent(eventType, data); err != nil {
			log.Printf("[EventDetector] Failed to send event, buffering: %v", err)
			e.bufferEvent(event)
		}
	} else {
		log.Println("[EventDetector] Not connected, buffering event")
		e.bufferEvent(event)
	}
}

// bufferEvent writes an event to persistent storage
func (e *EventDetector) bufferEvent(event map[string]interface{}) {
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

// flushBufferedEvents sends all buffered events
func (e *EventDetector) flushBufferedEvents(ctx context.Context) {
	// Wait for connection
	for !e.connMgr.IsConnected() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
			continue
		}
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

	// Parse and send each event
	lines := string(data)
	count := 0
	for _, line := range splitLines(lines) {
		if line == "" {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			log.Printf("[EventDetector] Failed to parse buffered event: %v", err)
			continue
		}

		eventType, _ := event["event"].(string)
		eventData, _ := event["data"].(map[string]interface{})

		if err := e.connMgr.SendEvent(eventType, eventData); err != nil {
			log.Printf("[EventDetector] Failed to send buffered event: %v", err)
			break
		}

		count++
		time.Sleep(100 * time.Millisecond)
	}

	log.Printf("[EventDetector] Flushed %d buffered events", count)

	// Clear buffer file
	os.Remove(e.bufferPath)
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
