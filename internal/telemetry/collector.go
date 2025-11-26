package telemetry

import (
	"context"
	"fmt"
	"strconv"

	"github.com/go-redis/redis/v8"
)

// Collector reads comprehensive state from Redis
type Collector struct {
	redisClient *redis.Client
	ctx         context.Context
}

// NewCollector creates a new telemetry collector
func NewCollector(redisClient *redis.Client) *Collector {
	return &Collector{
		redisClient: redisClient,
	}
}

// CollectState reads all telemetry fields from Redis (~50 fields)
func (c *Collector) CollectState(ctx context.Context) (map[string]interface{}, error) {
	c.ctx = ctx

	state := make(map[string]interface{})

	// Read all Redis keys and flatten into state map
	keys := []string{
		"vehicle", "battery:0", "battery:1", "aux-battery", "cb-battery",
		"engine-ecu", "power-manager", "internet", "modem", "gps",
		"keycard", "ble", "dashboard", "system",
	}

	for _, key := range keys {
		keyState, _ := c.collectKey(ctx, key)
		for field, value := range keyState {
			state[fmt.Sprintf("%s.%s", key, field)] = value
		}
	}

	return state, nil
}

// CollectKeyState reads state for a single Redis key
func (c *Collector) CollectKeyState(ctx context.Context, keyName string) (map[string]interface{}, error) {
	keyData, err := c.collectKey(ctx, keyName)
	if err != nil {
		return nil, err
	}

	// Convert to dot-notation keys
	state := make(map[string]interface{})
	for field, value := range keyData {
		state[fmt.Sprintf("%s.%s", keyName, field)] = value
	}

	return state, nil
}

// collectKey reads and parses a single Redis key
func (c *Collector) collectKey(ctx context.Context, keyName string) (map[string]interface{}, error) {
	data, err := c.redisClient.HGetAll(ctx, keyName).Result()
	if err != nil {
		return nil, err
	}

	result := make(map[string]interface{})

	// Parse based on key type
	switch keyName {
	case "gps":
		result["latitude"] = parseFloat(data["latitude"])
		result["longitude"] = parseFloat(data["longitude"])
		result["altitude"] = parseFloat(data["altitude"])
		result["state"] = data["state"]
		result["timestamp"] = data["timestamp"]

	case "vehicle":
		result["state"] = data["state"]
		result["handlebar_lock"] = data["handlebar:lock-sensor"]
		result["seatbox_lock"] = data["seatbox:lock"]
		result["kickstand"] = data["kickstand"]

	case "battery:0", "battery:1":
		result["present"] = data["present"] == "true"
		result["charge"] = parseInt(data["charge"])
		result["state"] = data["state"]
		result["voltage"] = parseInt(data["voltage"])
		result["soh"] = parseInt(data["state-of-health"])
		result["cycle_count"] = parseInt(data["cycle-count"])
		result["temp0"] = parseInt(data["temperature:0"])
		result["temp1"] = parseInt(data["temperature:1"])
		result["temp2"] = parseInt(data["temperature:2"])
		result["temp3"] = parseInt(data["temperature:3"])
		result["fw_version"] = data["fw-version"]
		result["serial"] = data["serial-number"]

	case "aux-battery":
		result["charge"] = parseInt(data["charge"])
		result["voltage"] = parseInt(data["voltage"])
		result["status"] = data["charge-status"]

	case "cb-battery":
		result["present"] = data["present"] == "true"
		result["charge"] = parseInt(data["charge"])
		result["status"] = data["charge-status"]

	case "engine-ecu":
		result["speed"] = parseInt(data["speed"])
		result["odometer"] = parseInt(data["odometer"])
		result["temperature"] = parseInt(data["temperature"])
		result["state"] = data["state"]
		result["fw_version"] = data["fw-version"]

	case "power-manager":
		result["state"] = data["state"]
		result["wakeup_source"] = data["wakeup-source"]
		result["nrf_reset_count"] = parseInt(data["nrf-reset-count"])

	case "internet":
		result["status"] = data["status"]
		result["signal_quality"] = parseInt(data["signal-quality"])
		result["ip_address"] = data["ip-address"]

	case "modem":
		result["operator"] = data["operator-name"]
		result["roaming"] = data["is-roaming"] == "true"
		result["sim_imei"] = data["sim-imei"]
		result["sim_imsi"] = data["sim-imsi"]
		result["sim_iccid"] = data["sim-iccid"]

	case "system":
		result["mdb_version"] = data["mdb-version"]
		result["dbc_version"] = data["dbc-version"]
		result["nrf_version"] = data["nrf-fw-version"]

	case "dashboard":
		result["serial"] = data["serial-number"]
		result["mode"] = data["mode"]

	case "keycard":
		result["authentication"] = data["authentication"]
		result["uid"] = data["uid"]

	case "ble":
		result["mac_address"] = data["mac-address"]
		result["status"] = data["status"]
	}

	return result, nil
}

// Helper functions
func parseInt(s string) int {
	val, _ := strconv.Atoi(s)
	return val
}

func parseFloat(s string) float64 {
	val, _ := strconv.ParseFloat(s, 64)
	return val
}
