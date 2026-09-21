package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v2"
)

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

	if err := c.SetField("events.movement.enabled", "false"); err != nil {
		t.Fatalf("set movement enabled: %v", err)
	}
	if c.Events.Movement.MovementEnabled() {
		t.Errorf("expected movement disabled")
	}
}

func TestSaveAtomicallyReplacesConfigAndBacksUpPreviousContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "uplink.yaml")
	const previous = "environment: development\n"
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Config{SourcePath: path, Environment: "production"}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	backup, err := os.ReadFile(path + ".backup")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != previous {
		t.Errorf("backup = %q, want %q", backup, previous)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var saved Config
	if err := yaml.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}
	if saved.Environment != "production" {
		t.Errorf("saved environment = %q, want production", saved.Environment)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %o, want 600", info.Mode().Perm())
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
