package telemetry

import (
	"reflect"
	"sort"
	"testing"
)

func TestDiffFlat(t *testing.T) {
	old := map[string]string{
		"battery:0.charge":  "64",
		"battery:0.voltage": "54000",
		"gps.latitude":      "52.5",
	}
	current := map[string]string{
		"battery:0.charge":  "65",
		"battery:0.voltage": "54000",
		"vehicle.state":     "parked",
	}

	changed, removed := diffFlat(old, current)

	wantChanged := map[string]string{
		"battery:0.charge": "65",
		"vehicle.state":    "parked",
	}
	if !reflect.DeepEqual(changed, wantChanged) {
		t.Errorf("changed = %v, want %v", changed, wantChanged)
	}
	if len(removed) != 1 || removed[0] != "gps.latitude" {
		t.Errorf("removed = %v, want [gps.latitude]", removed)
	}
}

func TestFlattenNestRoundTrip(t *testing.T) {
	state := map[string]any{
		"battery:0": map[string]any{"charge": "64", "seatbox:lock": "closed"},
		"gps":       map[string]any{"latitude": "52.5"},
	}
	flat := flattenState(state)
	if flat["battery:0.charge"] != "64" || flat["battery:0.seatbox:lock"] != "closed" {
		t.Fatalf("flatten wrong: %v", flat)
	}
	nested := nestFlat(flat)
	b0 := nested["battery:0"].(map[string]any)
	if b0["charge"] != "64" || b0["seatbox:lock"] != "closed" {
		t.Errorf("nest roundtrip lost fields: %v", nested)
	}
}

func TestQuantize(t *testing.T) {

	if got := quantize("battery:0[voltage]", "54049"); got != "54000" {
		t.Errorf("quantize 54049 = %s, want 54000", got)
	}
	if got := quantize("battery:0[voltage]", "54051"); got != "54100" {
		t.Errorf("quantize 54051 = %s, want 54100", got)
	}

	if got := quantize("vehicle[state]", "parked"); got != "parked" {
		t.Errorf("quantize passthrough = %s, want parked", got)
	}
}

func TestSmoothParkedGPSHoldsJitter(t *testing.T) {
	p := &Publisher{}

	snap1 := map[string]any{
		"vehicle": map[string]any{"state": "parked"},
		"gps":     map[string]any{"latitude": "52.500000", "longitude": "13.400000"},
	}
	p.smoothParkedGPS(snap1)

	snap2 := map[string]any{
		"vehicle": map[string]any{"state": "parked"},
		"gps":     map[string]any{"latitude": "52.500010", "longitude": "13.400010"},
	}
	p.smoothParkedGPS(snap2)
	gps := snap2["gps"].(map[string]any)
	if gps["latitude"] != "52.5" && gps["latitude"] != "52.500000" {

		t.Errorf("expected held latitude near anchor, got %v", gps["latitude"])
	}
}

func TestSmoothParkedGPSReleasesRealMove(t *testing.T) {
	p := &Publisher{}
	p.smoothParkedGPS(map[string]any{
		"vehicle": map[string]any{"state": "parked"},
		"gps":     map[string]any{"latitude": "52.500000", "longitude": "13.400000"},
	})

	snap := map[string]any{
		"vehicle": map[string]any{"state": "parked"},
		"gps":     map[string]any{"latitude": "52.510000", "longitude": "13.400000"},
	}
	p.smoothParkedGPS(snap)
	gps := snap["gps"].(map[string]any)

	if gps["latitude"] != "52.510000" {
		t.Errorf("expected moved latitude reported unchanged, got %v", gps["latitude"])
	}
}

func TestDiffFlatStableRemovedOrder(t *testing.T) {
	old := map[string]string{"a.x": "1", "a.y": "2", "a.z": "3"}
	current := map[string]string{}
	_, removed := diffFlat(old, current)
	sort.Strings(removed)
	if !reflect.DeepEqual(removed, []string{"a.x", "a.y", "a.z"}) {
		t.Errorf("removed = %v", removed)
	}
}
