package telemetry

import (
	"context"

	ipc "github.com/librescoot/redis-ipc"

	"github.com/librescoot/uplink-service/internal/config"
	"github.com/librescoot/uplink-service/internal/hwid"
	"github.com/librescoot/uplink-service/internal/modeminfo"
)

var collectedHashes = []string{
	"vehicle", "battery:0", "battery:1", "aux-battery", "cb-battery",
	"engine-ecu", "power-manager", "power-mux", "internet", "modem", "gps",
	"keycard", "ble", "dashboard", "system", "ota", "alarm", "navigation",
	"scooter",
}

type Collector struct {
	client  *ipc.Client
	version string
	cfg     *config.Config
	modem   *modeminfo.Poller

	boardSerial string
	ctx         context.Context
}

func NewCollector(client *ipc.Client, version string, cfg *config.Config, modem *modeminfo.Poller) *Collector {
	return &Collector{
		client:      client,
		version:     version,
		cfg:         cfg,
		modem:       modem,
		boardSerial: hwid.BoardSerial(),
	}
}

func (c *Collector) CollectState(ctx context.Context) (map[string]any, error) {
	c.ctx = ctx

	state := make(map[string]any)

	for _, key := range collectedHashes {
		keyState, _ := c.collectKey(ctx, key)
		if len(keyState) > 0 {
			state[key] = keyState
		}
	}

	// The cloud protocol does not recognize hop-on vehicle states.
	if vehicle, ok := state["vehicle"].(map[string]any); ok {
		if raw, hasState := vehicle["state"].(string); hasState {
			vehicle["state"] = cloudifyVehicleState(raw)
		}
	}

	c.augment(state)

	return state, nil
}

func (c *Collector) augment(state map[string]any) {

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

	if c.boardSerial != "" {
		system, ok := state["system"].(map[string]any)
		if !ok {
			system = make(map[string]any)
			state["system"] = system
		}
		system["mdb-serial"] = c.boardSerial
	}

	meta := map[string]any{
		"build-version": c.version,
	}
	if c.cfg != nil {
		meta["environment"] = c.cfg.Environment
		meta["identifier"] = c.cfg.Scooter.Identifier
	}
	state["meta"] = meta
}

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
