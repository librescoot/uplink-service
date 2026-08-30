package commands

import (
	"context"
	"fmt"
	"time"
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

func (h *Handler) navigate(params map[string]any) error {
	lat, hasLat := params["latitude"]
	lng, hasLng := params["longitude"]
	addr, _ := params["address"].(string)

	if !hasLat && !hasLng && addr == "" {
		if _, err := h.client.Raw().HDel(h.ctx, "navigation", "latitude", "longitude", "address", "timestamp").Result(); err != nil {
			return fmt.Errorf("clear navigation: %w", err)
		}
		_, _ = h.client.Publish("navigation", "cleared")
		return nil
	}

	set := func(field string, value any) error {
		return h.client.HSet("navigation", field, fmt.Sprint(value))
	}
	if hasLat {
		if err := set("latitude", lat); err != nil {
			return err
		}
	}
	if hasLng {
		if err := set("longitude", lng); err != nil {
			return err
		}
	}
	if addr != "" {
		if err := set("address", addr); err != nil {
			return err
		}
	}
	_ = set("timestamp", time.Now().UTC().Format(time.RFC3339))
	_, _ = h.client.Publish("navigation", "updated")
	return nil
}

// This diagnostic escape hatch is gated by command configuration.
func (h *Handler) redisCommand(params map[string]any) (map[string]any, error) {
	op, _ := params["command"].(string)
	args := toStringSlice(params["args"])

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
