package telemetry

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/librescoot/uplink-service/internal/connection"
	"github.com/librescoot/uplink-service/internal/geo"
	"github.com/librescoot/uplink-service/internal/timeutil"
)

// fullResyncEvery forces a full snapshot after this many consecutive deltas so
// the server's view cannot drift indefinitely.
const fullResyncEvery = 20

// parkedGPSSmoothingMeters is the distance a parked scooter's GPS fix must move
// before we report a new position, suppressing stationary jitter.
const parkedGPSSmoothingMeters = 5.0

// TelemetrySink is the subset of the connection manager the publisher needs.
type TelemetrySink interface {
	IsConnected() bool
	SendState(data map[string]any) error
	SendTelemetryDelta(changes map[string]any, removed []string) error
}

// TelemetryBuffer is the subset of the offline buffer the publisher needs.
type TelemetryBuffer interface {
	Add(snapshot map[string]any, timestamp string)
}

// Publisher is the single serialization point for telemetry. It diffs each
// fresh snapshot against the last one sent and emits either a full state
// message or a sparse delta (with removed paths), forcing a full resync on the
// first send, on vehicle-state changes, and periodically.
type Publisher struct {
	collector *Collector
	sink      TelemetrySink
	clock     *timeutil.Clock
	buffer    TelemetryBuffer // may be nil

	mu               sync.Mutex
	lastSentFlat     map[string]string
	lastVehicleState string
	flushesSinceFull int

	lastGPSLat   float64
	lastGPSLng   float64
	lastGPSValid bool
}

// NewPublisher creates a telemetry publisher. buffer may be nil to disable
// offline buffering.
func NewPublisher(collector *Collector, sink TelemetrySink, clock *timeutil.Clock, buffer TelemetryBuffer) *Publisher {
	return &Publisher{
		collector: collector,
		sink:      sink,
		clock:     clock,
		buffer:    buffer,
	}
}

// compile-time assertion that *connection.Manager satisfies TelemetrySink.
var _ TelemetrySink = (*connection.Manager)(nil)

// Flush collects a fresh snapshot and publishes it as a full or delta message.
func (p *Publisher) Flush(ctx context.Context, forceFull bool) error {
	snapshot, err := p.collector.CollectState(ctx)
	if err != nil {
		return fmt.Errorf("collect state: %w", err)
	}
	return p.Publish(snapshot, forceFull)
}

// ResetBaseline forces the next publish to be a full snapshot. Call on
// disconnect so the reconnect send re-syncs the server from scratch.
func (p *Publisher) ResetBaseline() {
	p.mu.Lock()
	p.lastSentFlat = nil
	p.mu.Unlock()
}

// Publish emits a snapshot as full state or a delta.
func (p *Publisher) Publish(snapshot map[string]any, forceFull bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.smoothParkedGPS(snapshot)
	flat := flattenState(snapshot)
	vehicleState := stateOf(snapshot)

	// When offline, buffer the full snapshot and arrange for a full resync on
	// reconnect. Do not attempt to send.
	if !p.sink.IsConnected() {
		if p.buffer != nil {
			p.buffer.Add(snapshot, p.clock.Now())
		}
		p.lastSentFlat = nil
		return nil
	}

	full := forceFull ||
		p.lastSentFlat == nil ||
		vehicleState != p.lastVehicleState ||
		p.flushesSinceFull >= fullResyncEvery

	if full {
		if err := p.sink.SendState(snapshot); err != nil {
			return err
		}
		p.lastSentFlat = flat
		p.lastVehicleState = vehicleState
		p.flushesSinceFull = 0
		return nil
	}

	changed, removed := diffFlat(p.lastSentFlat, flat)
	if len(changed) == 0 && len(removed) == 0 {
		return nil
	}
	if err := p.sink.SendTelemetryDelta(nestFlat(changed), removed); err != nil {
		return err
	}
	p.lastSentFlat = flat
	p.lastVehicleState = vehicleState
	p.flushesSinceFull++
	return nil
}

// smoothParkedGPS holds the last reported position while the scooter is not
// actively driving, until the fix moves beyond the smoothing threshold.
func (p *Publisher) smoothParkedGPS(snapshot map[string]any) {
	gps, ok := snapshot["gps"].(map[string]any)
	if !ok {
		return
	}
	lat, latOK := parseFloatField(gps, "latitude")
	lng, lngOK := parseFloatField(gps, "longitude")
	if !latOK || !lngOK {
		return
	}

	driving := stateOf(snapshot) == "ready-to-drive"
	if !driving && p.lastGPSValid {
		if geo.HaversineMeters(p.lastGPSLat, p.lastGPSLng, lat, lng) < parkedGPSSmoothingMeters {
			// Report the held position instead of the jittered one.
			gps["latitude"] = formatFloat(p.lastGPSLat)
			gps["longitude"] = formatFloat(p.lastGPSLng)
			return
		}
	}

	p.lastGPSLat = lat
	p.lastGPSLng = lng
	p.lastGPSValid = true
}

func stateOf(snapshot map[string]any) string {
	if vehicle, ok := snapshot["vehicle"].(map[string]any); ok {
		if s, ok := vehicle["state"].(string); ok {
			return s
		}
	}
	return ""
}

// flattenState converts a two-level snapshot into "hash.field" -> value pairs.
func flattenState(state map[string]any) map[string]string {
	out := make(map[string]string)
	for hash, v := range state {
		fields, ok := v.(map[string]any)
		if !ok {
			continue
		}
		for f, val := range fields {
			out[hash+"."+f] = fmt.Sprint(val)
		}
	}
	return out
}

// nestFlat rebuilds a two-level nested map from "hash.field" -> value pairs.
func nestFlat(flat map[string]string) map[string]any {
	out := make(map[string]any)
	for path, val := range flat {
		idx := strings.Index(path, ".")
		if idx < 0 {
			continue
		}
		hash, field := path[:idx], path[idx+1:]
		m, ok := out[hash].(map[string]any)
		if !ok {
			m = make(map[string]any)
			out[hash] = m
		}
		m[field] = val
	}
	return out
}

// diffFlat returns the changed leaves and the paths present in old but not new.
func diffFlat(old, current map[string]string) (changed map[string]string, removed []string) {
	changed = make(map[string]string)
	for k, v := range current {
		if ov, ok := old[k]; !ok || ov != v {
			changed[k] = v
		}
	}
	for k := range old {
		if _, ok := current[k]; !ok {
			removed = append(removed, k)
		}
	}
	return changed, removed
}

func parseFloatField(m map[string]any, field string) (float64, bool) {
	s, ok := m[field].(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
