package telemetry

import (
	"context"

	ipc "github.com/librescoot/redis-ipc"

	"github.com/librescoot/uplink-service/internal/config"
	"github.com/librescoot/uplink-service/internal/hwid"
	"github.com/librescoot/uplink-service/internal/modeminfo"
)

// collectedHashes are the Redis hashes read into a full telemetry snapshot.
// All fields of each hash are passed through unchanged.
var collectedHashes = []string{
	"vehicle", "battery:0", "battery:1", "aux-battery", "cb-battery",
	"engine-ecu", "power-manager", "power-mux", "internet", "modem", "gps",
	"keycard", "ble", "dashboard", "system", "ota", "alarm", "navigation",
	"scooter",
}

// Collector reads comprehensive state from Redis and augments it with
// process/hardware identity.
type Collector struct {
	client  *ipc.Client
	version string
	cfg     *config.Config
	modem   *modeminfo.Poller

	boardSerial string
	ctx         context.Context
}

// NewCollector creates a new telemetry collector. modem may be nil (identity
// fields are then omitted).
func NewCollector(client *ipc.Client, version string, cfg *config.Config, modem *modeminfo.Poller) *Collector {
	return &Collector{
		client:      client,
		version:     version,
		cfg:         cfg,
		modem:       modem,
		boardSerial: hwid.BoardSerial(),
	}
}

// CollectState reads all telemetry hashes from Redis and augments the snapshot
// with modem identity, the board serial, and process metadata.
func (c *Collector) CollectState(ctx context.Context) (map[string]any, error) {
	c.ctx = ctx

	state := make(map[string]any)

	for _, key := range collectedHashes {
		keyState, _ := c.collectKey(ctx, key)
		if len(keyState) > 0 {
			state[key] = keyState
		}
	}

	// vehicle-service publishes the leaf hop-on states directly. Cloud
	// consumers only know about the classic states, so collapse "hop-on" ->
	// "stand-by" (locked) and "hop-on-learning" -> "parked". Mirrors the remap
	// applied incrementally in Monitor.handleFieldChange.
	if vehicle, ok := state["vehicle"].(map[string]any); ok {
		if raw, hasState := vehicle["state"].(string); hasState {
			vehicle["state"] = cloudifyVehicleState(raw)
		}
	}

	c.augment(state)

	return state, nil
}

// augment merges synthesized identity/metadata into a snapshot.
func (c *Collector) augment(state map[string]any) {
	// Modem identity fields merge into the existing "modem" hash.
	if c.modem != nil {
		if fields := c.modem.AsFields(); len(fields) > 0 {
			modem, ok := state["modem"].(map[string]any)
			if !ok {
				modem = make(map[string]any)
				state["modem"] = modem
			}
			for k, v := range fields {
				modem[k] = v
			}
		}
	}

	// Board serial merges into "system".
	if c.boardSerial != "" {
		system, ok := state["system"].(map[string]any)
		if !ok {
			system = make(map[string]any)
			state["system"] = system
		}
		system["mdb-serial"] = c.boardSerial
	}

	// Process metadata. Deliberately curated (no secrets) rather than dumping
	// the whole config, which now holds notification credentials.
	meta := map[string]any{
		"build-version": c.version,
	}
	if c.cfg != nil {
		meta["environment"] = c.cfg.Environment
		meta["identifier"] = c.cfg.Scooter.Identifier
	}
	state["meta"] = meta
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

// cloudifyVehicleState maps the hop-on family of states to their cloud-facing
// equivalents. All other states pass through unchanged.
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
