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

// Periodic full state bounds server/client drift from missed deltas.
const fullResyncEvery = 20

// Parked fixes must move this far before telemetry reports a new position.
const parkedGPSSmoothingMeters = 5.0

type TelemetrySink interface {
	IsConnected() bool
	SendState(data map[string]any) error
	SendTelemetryDelta(changes map[string]any, removed []string) error
}

type TelemetryBuffer interface {
	Add(snapshot map[string]any, timestamp string)
}

type Publisher struct {
	collector *Collector
	sink      TelemetrySink
	clock     *timeutil.Clock
	buffer    TelemetryBuffer

	mu               sync.Mutex
	lastSentFlat     map[string]string
	lastVehicleState string
	flushesSinceFull int

	lastGPSLat   float64
	lastGPSLng   float64
	lastGPSValid bool
}

func NewPublisher(collector *Collector, sink TelemetrySink, clock *timeutil.Clock, buffer TelemetryBuffer) *Publisher {
	return &Publisher{
		collector: collector,
		sink:      sink,
		clock:     clock,
		buffer:    buffer,
	}
}

var _ TelemetrySink = (*connection.Manager)(nil)

func (p *Publisher) Flush(ctx context.Context, forceFull bool) error {
	snapshot, err := p.collector.CollectState(ctx)
	if err != nil {
		return fmt.Errorf("collect state: %w", err)
	}
	return p.Publish(snapshot, forceFull)
}

// ResetBaseline makes reconnect send state, never a delta with an unknown server base.
func (p *Publisher) ResetBaseline() {
	p.mu.Lock()
	p.lastSentFlat = nil
	p.mu.Unlock()
}

// Publish serializes snapshots and deltas; removed paths are transmitted because
// a merge-only delta cannot delete a server-side field.
func (p *Publisher) Publish(snapshot map[string]any, forceFull bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.smoothParkedGPS(snapshot)
	flat := flattenState(snapshot)
	vehicleState := stateOf(snapshot)

	if !p.sink.IsConnected() {
		// Buffer full snapshots; deltas require the server baseline.
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

// smoothParkedGPS suppresses stationary jitter but never masks movement while driving.
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
	// Suppress stationary GPS jitter without hiding movement.
	if !driving && p.lastGPSValid {
		if geo.HaversineMeters(p.lastGPSLat, p.lastGPSLng, lat, lng) < parkedGPSSmoothingMeters {

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
