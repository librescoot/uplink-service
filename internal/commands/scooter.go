package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

func paramFloat(v any, def float64) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return def
}

func (h *Handler) locate() error {
	honk := time.Duration(paramFloat(h.cfg.CommandParam("locate", "honk_time", 40), def40)) * time.Millisecond
	gap := time.Duration(paramFloat(h.cfg.CommandParam("locate", "honk_interval", 80), def80)) * time.Millisecond
	burstGap := time.Duration(paramFloat(h.cfg.CommandParam("locate", "interval", 4000), def4000)) * time.Millisecond

	if err := h.sendCommand("scooter:blinker", "both"); err != nil {
		return err
	}

	go func() {
		defer func() { _ = h.sendCommand("scooter:blinker", "off") }()
		for burst := 0; burst < 2; burst++ {
			for beep := 0; beep < 2; beep++ {
				if !h.beep(honk) {
					return
				}
				if !sleepOrDone(h.ctx, gap) {
					return
				}
			}
			if burst == 0 && !sleepOrDone(h.ctx, burstGap) {
				return
			}
		}
	}()
	return nil
}

func (h *Handler) beep(d time.Duration) bool {
	if err := h.sendCommand("scooter:horn", "on"); err != nil {
		return false
	}
	ok := sleepOrDone(h.ctx, d)
	_ = h.sendCommand("scooter:horn", "off")
	return ok
}

func (h *Handler) alarmPulse(params map[string]any) error {
	if state, _ := params["state"].(string); state == "off" {
		h.stopAlarm()
		return nil
	}

	duration := time.Duration(paramFloat(params["duration"], def10000)) * time.Millisecond
	onTime := time.Duration(paramFloat(h.cfg.CommandParam("alarm", "on_time", 400), def400)) * time.Millisecond
	offTime := time.Duration(paramFloat(h.cfg.CommandParam("alarm", "off_time", 400), def400)) * time.Millisecond

	ctx, cancel := context.WithCancel(h.ctx)
	gen := h.startAlarm(cancel)

	go func() {
		defer h.clearAlarm(gen)
		_ = h.sendCommand("scooter:blinker", "both")
		defer func() {
			_ = h.sendCommand("scooter:horn", "off")
			_ = h.sendCommand("scooter:blinker", "off")
		}()

		deadline := time.NewTimer(duration)
		defer deadline.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-deadline.C:
				return
			default:
			}
			_ = h.sendCommand("scooter:horn", "on")
			if !sleepOrCtx(ctx, deadline, onTime) {
				return
			}
			_ = h.sendCommand("scooter:horn", "off")
			if !sleepOrCtx(ctx, deadline, offTime) {
				return
			}
		}
	}()
	return nil
}

func (h *Handler) startAlarm(cancel context.CancelFunc) int {
	h.alarmMu.Lock()
	prev := h.alarmCancel
	h.alarmGen++
	gen := h.alarmGen
	h.alarmCancel = cancel
	h.alarmMu.Unlock()
	if prev != nil {
		prev()
	}
	return gen
}

func (h *Handler) clearAlarm(gen int) {
	h.alarmMu.Lock()
	if h.alarmGen == gen {
		h.alarmCancel = nil
	}
	h.alarmMu.Unlock()
}

func (h *Handler) stopAlarm() {
	h.alarmMu.Lock()
	cancel := h.alarmCancel
	h.alarmCancel = nil
	h.alarmMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

const routePlanChannel = "settings:route-plan"

type routeStop struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Label string  `json:"label"`
}

type routePlan struct {
	ID          string      `json:"id"`
	Revision    uint64      `json:"revision"`
	Stops       []routeStop `json:"stops"`
	CurrentStep int         `json:"current_step"`
}

type replaceRoutePlanRequest struct {
	Stops []routeStop `json:"stops"`
}

func (h *Handler) navigate(params map[string]any) error {
	addr, _ := params["address"].(string)
	stops, hasWaypoints := buildWaypoints(params)
	if _, supplied := params["waypoints"]; supplied && !hasWaypoints {
		return fmt.Errorf("invalid route waypoints")
	}
	if hasWaypoints {
		capabilities, err := h.client.HGet("system", "capabilities")
		if err != nil || !hasNavigationRouteCapability(capabilities) {
			return fmt.Errorf("scooter does not advertise multi-stop routes")
		}
	} else if params["latitude"] == nil && params["longitude"] == nil && addr == "" {
		_, err := ipc.CallMethod[struct{}, routePlan](h.client, routePlanChannel, "plan.clear", struct{}{}, 3*time.Second)
		if err != nil {
			return fmt.Errorf("clear route plan: %w", err)
		}
		return nil
	} else {
		lat, okLat := firstNumber(params, "latitude")
		lon, okLon := firstNumber(params, "longitude")
		if !okLat || !okLon {
			return fmt.Errorf("navigate requires both latitude and longitude")
		}
		stops = []routeStop{{Lat: lat, Lon: lon, Label: addr}}
	}
	_, err := ipc.CallMethod[replaceRoutePlanRequest, routePlan](h.client, routePlanChannel, "plan.replace", replaceRoutePlanRequest{Stops: stops}, 3*time.Second)
	if err != nil {
		return fmt.Errorf("replace route plan: %w", err)
	}
	return nil
}

func hasNavigationRouteCapability(capabilities string) bool {
	if !strings.HasPrefix(capabilities, "cap:ext:") {
		return false
	}
	for _, group := range strings.Split(strings.TrimPrefix(capabilities, "cap:ext:"), ":") {
		if group == "nav=2" {
			return true
		}
	}
	return false
}

// buildWaypoints reads an optional ordered stop list from a navigate command.
// Stops accept latitude/longitude or lat/lon (number or numeric string).
func buildWaypoints(params map[string]any) ([]routeStop, bool) {
	raw, present := params["waypoints"]
	if !present {
		return nil, false
	}
	list, isList := raw.([]any)
	if !isList || len(list) == 0 {
		return nil, false
	}
	stops := make([]routeStop, 0, len(list))
	for _, entry := range list {
		m, isMap := entry.(map[string]any)
		if !isMap {
			continue
		}
		la, okLat := firstNumber(m, "latitude", "lat")
		lo, okLon := firstNumber(m, "longitude", "lon")
		if !okLat || !okLon {
			continue
		}
		label, _ := m["label"].(string)
		if label == "" {
			label, _ = m["name"].(string)
		}
		stops = append(stops, routeStop{Lat: la, Lon: lo, Label: label})
	}
	if len(stops) == 0 {
		return nil, false
	}
	return stops, true
}

func firstNumber(m map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f, true
			}
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

// This diagnostic escape hatch is gated by command configuration.
func (h *Handler) redisCommand(params map[string]any) (map[string]any, error) {
	op, _ := params["command"].(string)
	args := toStringSlice(params["args"])
	if (op == "set" || op == "hset") && len(args) > 0 && args[0] == "navigation" || op == "del" && slices.Contains(args, "navigation") {
		return nil, fmt.Errorf("navigation is owned by settings-service")
	}

	switch op {
	case "get":
		if len(args) < 1 {
			return nil, fmt.Errorf("get requires a key")
		}
		v, err := h.client.Get(args[0])
		return map[string]any{"value": v}, err
	case "set":
		if len(args) < 2 {
			return nil, fmt.Errorf("set requires key and value")
		}
		return nil, h.client.Set(args[0], args[1], 0)
	case "hget":
		if len(args) < 2 {
			return nil, fmt.Errorf("hget requires key and field")
		}
		v, err := h.client.HGet(args[0], args[1])
		return map[string]any{"value": v}, err
	case "hset":
		if len(args) < 3 {
			return nil, fmt.Errorf("hset requires key, field and value")
		}
		return nil, h.client.HSet(args[0], args[1], args[2])
	case "hgetall":
		if len(args) < 1 {
			return nil, fmt.Errorf("hgetall requires a key")
		}
		m, err := h.client.HGetAll(args[0])
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return map[string]any{"value": out}, err
	case "del":
		if len(args) < 1 {
			return nil, fmt.Errorf("del requires a key")
		}
		n, err := h.client.Del(args...)
		return map[string]any{"deleted": n}, err
	case "lpush":
		if len(args) < 2 {
			return nil, fmt.Errorf("lpush requires key and value")
		}
		n, err := h.client.LPush(args[0], args[1])
		return map[string]any{"length": n}, err
	case "publish":
		if len(args) < 2 {
			return nil, fmt.Errorf("publish requires channel and message")
		}
		n, err := h.client.Publish(args[0], args[1])
		return map[string]any{"receivers": n}, err
	default:
		return nil, fmt.Errorf("unsupported redis op: %q", op)
	}
}

func toStringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

func sleepOrCtx(ctx context.Context, deadline *time.Timer, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	case <-deadline.C:

		return false
	}
}

const (
	def40    = 40
	def80    = 80
	def400   = 400
	def4000  = 4000
	def10000 = 10000
)
