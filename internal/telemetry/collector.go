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
		if len(keyState) > 0 {
			state[key] = keyState
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

	state := make(map[string]interface{})
	if len(keyData) > 0 {
		state[keyName] = keyData
	}

	return state, nil
}

// collectKey reads a single Redis key, passing through all fields
func (c *Collector) collectKey(ctx context.Context, keyName string) (map[string]interface{}, error) {
	data, err := c.client.HGetAll(keyName)
	if err != nil {
		return nil, err
	}

	result := make(map[string]interface{}, len(data))
	for field, value := range data {
		result[field] = value
	}
	return result, nil
}

