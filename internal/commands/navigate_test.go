package commands

import (
	"encoding/json"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	ipc "github.com/librescoot/redis-ipc"
)

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
			wantJSON: `[{"lat":52.51,"lon":13.41,"label":""}]`,
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
			stops, gotOK := buildWaypoints(tt.params)
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if stops[0].Lat != tt.wantLat || stops[0].Lon != tt.wantLng {
				t.Errorf("first stop = %+v want %v,%v", stops[0], tt.wantLat, tt.wantLng)
			}
			if stops[0].Label != tt.wantLabel {
				t.Errorf("label = %q, want %q", stops[0].Label, tt.wantLabel)
			}
			var got, want any
			gotJSON, err := json.Marshal(stops)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(gotJSON, &got); err != nil {
				t.Fatal(err)
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

func TestNavigateRejectsPartialDestination(t *testing.T) {
	h := &Handler{}
	for _, params := range []map[string]any{
		{"latitude": 52.5}, {"longitude": 13.4}, {"address": "Home"},
		{"latitude": "invalid", "longitude": 13.4},
	} {
		if err := h.navigate(params); err == nil {
			t.Errorf("navigate(%v) accepted incomplete coordinates", params)
		}
	}
}

func TestNavigateCallsRoutePlanOwner(t *testing.T) {
	mr := miniredis.RunT(t)
	host, portStr, err := net.SplitHostPort(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ipc.New(ipc.WithAddress(host), ipc.WithPort(port))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	h := &Handler{client: client}
	server := ipc.NewCallServer(client, routePlanChannel, ipc.WithCallServerConcurrency(1))
	var received [][]routeStop
	clears := 0
	ipc.RegisterCall[replaceRoutePlanRequest, routePlan](server, "plan.replace", func(req replaceRoutePlanRequest) (routePlan, error) {
		received = append(received, req.Stops)
		return routePlan{ID: "owner-id", Revision: uint64(len(received))}, nil
	})
	ipc.RegisterCall[struct{}, routePlan](server, "plan.clear", func(_ struct{}) (routePlan, error) {
		clears++
		return routePlan{Revision: uint64(len(received) + clears)}, nil
	})
	server.Start()
	defer server.Stop()
	if err := h.navigate(map[string]any{"latitude": 52.5, "longitude": 13.4, "address": "Home"}); err != nil {
		t.Fatal(err)
	}
	if err := client.HSet("system", "capabilities", "cap:ext:nav=2"); err != nil {
		t.Fatal(err)
	}
	if err := h.navigate(map[string]any{"waypoints": []any{
		map[string]any{"lat": 52.5, "lon": 13.4, "label": "Home"},
		map[string]any{"latitude": "53.1", "longitude": "14.2", "name": "Work"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := h.navigate(nil); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || len(received[0]) != 1 || received[0][0] != (routeStop{52.5, 13.4, "Home"}) ||
		len(received[1]) != 2 || received[1][1] != (routeStop{53.1, 14.2, "Work"}) || clears != 1 {
		t.Fatalf("received stops %v, clears %d", received, clears)
	}
	if mr.Exists("navigation") {
		t.Fatal("navigate wrote navigation directly")
	}
}

func TestNavigateFailsWhenOwnerUnavailable(t *testing.T) {
	mr := miniredis.RunT(t)
	host, portStr, _ := net.SplitHostPort(mr.Addr())
	port, _ := strconv.Atoi(portStr)
	client, err := ipc.New(ipc.WithAddress(host), ipc.WithPort(port))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	h := &Handler{client: client}
	start := time.Now()
	if err := h.navigate(map[string]any{"latitude": 52.5, "longitude": 13.4}); err == nil {
		t.Fatal("missing owner accepted destination")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("owner timeout exceeded budget")
	}
	if mr.Exists("navigation") {
		t.Fatal("missing owner caused direct navigation write")
	}
}

func TestRedisCommandRejectsNavigationMutation(t *testing.T) {
	h := &Handler{}
	for _, params := range []map[string]any{
		{"command": "hset", "args": []any{"navigation", "plan", "{}"}},
		{"command": "set", "args": []any{"navigation", "{}"}},
		{"command": "del", "args": []any{"other", "navigation"}},
	} {
		if _, err := h.redisCommand(params); err == nil {
			t.Errorf("redis command %v changed navigation", params)
		}
	}
}
