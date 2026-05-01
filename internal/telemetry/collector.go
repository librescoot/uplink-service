package telemetry

import (
	"context"

	ipc "github.com/librescoot/redis-ipc"
)

// Collector reads comprehensive state from Redis
type Collector struct {
	client *ipc.Client
	ctx    context.Context
}

// NewCollector creates a new telemetry collector
func NewCollector(client *ipc.Client) *Collector {
	return &Collector{
		client: client,
	}
}

// CollectState reads all telemetry fields from Redis (~50 fields)
func (c *Collector) CollectState(ctx context.Context) (map[string]any, error) {
	c.ctx = ctx

	state := make(map[string]any)

	// Read all Redis keys and flatten into state map
	keys := []string{
		"vehicle", "battery:0", "battery:1", "aux-battery", "cb-battery",
		"engine-ecu", "power-manager", "internet", "modem", "gps",
		"keycard", "ble", "dashboard", "system", "ota", "alarm",
	}

	for _, key := range keys {
		keyState, _ := c.collectKey(ctx, key)
		if len(keyState) > 0 {
			state[key] = keyState
		}
	}

	// vehicle-service publishes the leaf hop-on states directly. Cloud
	// consumers (and indirectly the mobile app) only know about the
	// classic states, so collapse "hop-on" -> "stand-by" (locked) and
	// "hop-on-learning" -> "parked" (user is interacting). Mirrors the
	// remap applied incrementally in Monitor.handleFieldChange.
	if vehicle, ok := state["vehicle"].(map[string]any); ok {
		if raw, hasState := vehicle["state"].(string); hasState {
			vehicle["state"] = cloudifyVehicleState(raw)
		}
	}

	return state, nil
}

// cloudifyVehicleState maps the hop-on family of states to their
// cloud-facing equivalents. All other states pass through unchanged.
func cloudifyVehicleState(raw string) string {
	switch raw {
	case "hop-on":
		return "stand-by"
	case "hop-on-learning":
		return "parked"
	default:
		return raw
	}
}

// CollectKeyState reads state for a single Redis key
func (c *Collector) CollectKeyState(ctx context.Context, keyName string) (map[string]any, error) {
	keyData, err := c.collectKey(ctx, keyName)
	if err != nil {
		return nil, err
	}

	state := make(map[string]any)
	if len(keyData) > 0 {
		state[keyName] = keyData
	}

	return state, nil
}

// collectKey reads a single Redis key, passing through all fields
func (c *Collector) collectKey(ctx context.Context, keyName string) (map[string]any, error) {
	data, err := c.client.HGetAll(keyName)
	if err != nil {
		return nil, err
	}

	result := make(map[string]any, len(data))
	for field, value := range data {
		result[field] = value
	}
	return result, nil
}

