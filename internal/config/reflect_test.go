package config

import "testing"

func TestReflectionRoundTrip(t *testing.T) {
	c := &Config{}
	c.applyDefaults()

	if err := c.SetField("uplink.server_url", "ws://example:8080/ws"); err != nil {
		t.Fatalf("set server_url: %v", err)
	}
	v, err := c.GetField("uplink.server_url")
	if err != nil {
		t.Fatalf("get server_url: %v", err)
	}
	if v != "ws://example:8080/ws" {
		t.Errorf("server_url = %v", v)
	}

	if err := c.SetField("telemetry.buffer.max_size", "500"); err != nil {
		t.Fatalf("set max_size: %v", err)
	}
	if c.Telemetry.Buffer.MaxSize != 500 {
		t.Errorf("max_size = %d, want 500", c.Telemetry.Buffer.MaxSize)
	}

	if err := c.SetField("environment", "development"); err != nil {
		t.Fatalf("set environment: %v", err)
	}
	if !c.IsDevelopment() {
		t.Errorf("expected development mode")
	}

	if err := c.DeleteField("uplink.server_url"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if c.Uplink.ServerURL != "" {
		t.Errorf("delete did not zero the field: %q", c.Uplink.ServerURL)
	}

	// Pointer field (movement enabled) should be settable.
	if err := c.SetField("events.movement.enabled", "false"); err != nil {
		t.Fatalf("set movement enabled: %v", err)
	}
	if c.Events.Movement.MovementEnabled() {
		t.Errorf("expected movement disabled")
	}
}

func TestReflectionUnknownField(t *testing.T) {
	c := &Config{}
	if _, err := c.GetField("uplink.nope"); err == nil {
		t.Errorf("expected error for unknown field")
	}
}

func TestApplyDeltas(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	err := c.ApplyDeltas(map[string]string{
		"uplink.keepalive_interval": "30s",
		"scooter.identifier":        "VIN123",
	})
	if err != nil {
		t.Fatalf("apply deltas: %v", err)
	}
	if c.Uplink.KeepaliveInterval != "30s" || c.Scooter.Identifier != "VIN123" {
		t.Errorf("deltas not applied: %+v", c.Uplink)
	}
}
