package commands

import (
	"encoding/json"
	"testing"
)

// buildWaypoints is the parse side of a cloud-pushed multi-hop plan. The
// dashboard expects the lat/lon/label JSON shape, and the first stop is also
// published as the current target.
func TestHasNavigationRouteCapability(t *testing.T) {
	for _, tc := range []struct {
		registry string
		want     bool
	}{
		{"cap:ext:nav=2:keycard", true},
		{"cap:ext:keycard:nav=2", true},
		{"cap:ext:nav=1", false},
		{"cap:ext:nav=20", false},
		{"nav=2", false},
		{"", false},
	} {
		if got := hasNavigationRouteCapability(tc.registry); got != tc.want {
			t.Errorf("registry %q: got %t, want %t", tc.registry, got, tc.want)
		}
	}
}

func TestBuildWaypoints(t *testing.T) {
	tests := []struct {
		name      string
		params    map[string]any
		wantOK    bool
		wantJSON  string
		wantLat   float64
		wantLng   float64
		wantLabel string
	}{
		{
			name:   "absent",
			params: map[string]any{"latitude": 1.0, "longitude": 2.0},
			wantOK: false,
		},
		{
			name:   "empty list",
			params: map[string]any{"waypoints": []any{}},
			wantOK: false,
		},
		{
			name: "lat lon with label",
			params: map[string]any{"waypoints": []any{
				map[string]any{"lat": 52.51, "lon": 13.41, "label": "Work"},
				map[string]any{"lat": 52.52, "lon": 13.42, "label": "Gym"},
			}},
			wantOK:    true,
			wantJSON:  `[{"lat":52.51,"lon":13.41,"label":"Work"},{"lat":52.52,"lon":13.42,"label":"Gym"}]`,
			wantLat:   52.51,
			wantLng:   13.41,
			wantLabel: "Work",
		},
		{
			name: "latitude longitude and name",
			params: map[string]any{"waypoints": []any{
				map[string]any{"latitude": 52.51, "longitude": 13.41, "name": "Home"},
			}},
			wantOK:    true,
			wantJSON:  `[{"lat":52.51,"lon":13.41,"label":"Home"}]`,
			wantLat:   52.51,
			wantLng:   13.41,
			wantLabel: "Home",
		},
		{
			name: "numeric strings",
			params: map[string]any{"waypoints": []any{
				map[string]any{"lat": "52.51", "lon": "13.41"},
			}},
			wantOK:   true,
			wantJSON: `[{"lat":52.51,"lon":13.41}]`,
			wantLat:  52.51,
			wantLng:  13.41,
		},
		{
			name: "invalid entries skipped, one valid remains",
			params: map[string]any{"waypoints": []any{
				map[string]any{"lat": "nope", "lon": 13.41},
				"not an object",
				map[string]any{"lat": 52.52, "lon": 13.42, "label": "Keep"},
			}},
			wantOK:    true,
			wantJSON:  `[{"lat":52.52,"lon":13.42,"label":"Keep"}]`,
			wantLat:   52.52,
			wantLng:   13.42,
			wantLabel: "Keep",
		},
		{
			name: "all entries invalid",
			params: map[string]any{"waypoints": []any{
				map[string]any{"lat": 52.52},
				map[string]any{"foo": "bar"},
			}},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJSON, gotLat, gotLng, gotLabel, gotOK := buildWaypoints(tt.params)
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if gotLat != tt.wantLat || gotLng != tt.wantLng {
				t.Errorf("first stop = %v,%v want %v,%v", gotLat, gotLng, tt.wantLat, tt.wantLng)
			}
			if gotLabel != tt.wantLabel {
				t.Errorf("label = %q, want %q", gotLabel, tt.wantLabel)
			}
			var got, want any
			if err := json.Unmarshal([]byte(gotJSON), &got); err != nil {
				t.Fatalf("emitted JSON does not parse: %v (%s)", err, gotJSON)
			}
			if err := json.Unmarshal([]byte(tt.wantJSON), &want); err != nil {
				t.Fatalf("test fixture JSON does not parse: %v", err)
			}
			if !jsonEqual(got, want) {
				t.Errorf("json = %s, want %s", gotJSON, tt.wantJSON)
			}
		})
	}
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
