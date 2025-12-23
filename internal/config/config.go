package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v2"
)

// Config represents the uplink-service configuration
type Config struct {
	Uplink    UplinkConfig    `yaml:"uplink"`
	Scooter   ScooterConfig   `yaml:"scooter"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
	RedisURL  string          `yaml:"redis_url"`
}

// UplinkConfig contains uplink server connection settings
type UplinkConfig struct {
	ServerURL         string `yaml:"server_url"`
	FallbackURL       string `yaml:"fallback_url,omitempty"`
	KeepaliveInterval string `yaml:"keepalive_interval"`
	ReconnectMaxDelay string `yaml:"reconnect_max_delay"`
}

// ScooterConfig contains scooter identification
type ScooterConfig struct {
	Identifier string `yaml:"identifier"`
	Token      string `yaml:"token"`
}

// TelemetryConfig contains telemetry settings
type TelemetryConfig struct {
	EventBufferPath string `yaml:"event_buffer_path"`
	EventMaxRetries int    `yaml:"event_max_retries"`
}

// GetKeepaliveInterval parses and returns the keepalive interval
func (c *UplinkConfig) GetKeepaliveInterval() time.Duration {
	d, err := time.ParseDuration(c.KeepaliveInterval)
	if err != nil {
		return 5 * time.Minute
	}
	return d
}

// GetReconnectMaxDelay parses and returns the max reconnect delay
func (c *UplinkConfig) GetReconnectMaxDelay() time.Duration {
	d, err := time.ParseDuration(c.ReconnectMaxDelay)
	if err != nil {
		return 5 * time.Minute
	}
	return d
}

// Load loads configuration from a YAML file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Set defaults
	if config.Uplink.KeepaliveInterval == "" {
		config.Uplink.KeepaliveInterval = "5m"
	}
	if config.Uplink.ReconnectMaxDelay == "" {
		config.Uplink.ReconnectMaxDelay = "5m"
	}
	if config.Telemetry.EventBufferPath == "" {
		config.Telemetry.EventBufferPath = "/data/uplink-events.queue"
	}
	if config.Telemetry.EventMaxRetries == 0 {
		config.Telemetry.EventMaxRetries = 5
	}

	return &config, nil
}
