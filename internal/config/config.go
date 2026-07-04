package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// Config represents the uplink-service configuration
type Config struct {
	Uplink        UplinkConfig        `yaml:"uplink"`
	Scooter       ScooterConfig       `yaml:"scooter"`
	Telemetry     TelemetryConfig     `yaml:"telemetry"`
	Events        EventsConfig        `yaml:"events"`
	Notifications NotificationsConfig `yaml:"notifications"`
	Commands      map[string]CommandConfig `yaml:"commands"`
	NTP           NTPConfig           `yaml:"ntp"`
	Environment   string              `yaml:"environment"`
	ServiceName   string              `yaml:"service_name"`
	RedisURL      string              `yaml:"redis_url"`

	// SourcePath is the on-disk path this config was loaded from. It is not
	// part of the YAML document; it is recorded at load time so runtime config
	// mutations (config:set / config:save / config_update) can persist back to
	// the same file. See reflect.go.
	SourcePath string `yaml:"-"`
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
	Name       string `yaml:"name,omitempty"`
}

// TelemetryConfig contains telemetry settings
type TelemetryConfig struct {
	EventBufferPath string          `yaml:"event_buffer_path"`
	EventMaxRetries int             `yaml:"event_max_retries"`
	TransmitPeriod  string          `yaml:"transmit_period"`
	Buffer          BufferConfig    `yaml:"buffer"`
	Intervals       IntervalsConfig `yaml:"intervals"`
}

// BufferConfig controls the offline telemetry buffer.
type BufferConfig struct {
	Enabled       bool   `yaml:"enabled"`
	MaxSize       int    `yaml:"max_size"`
	MaxRetries    int    `yaml:"max_retries"`
	RetryInterval string `yaml:"retry_interval"`
	PersistPath   string `yaml:"persist_path"`
}

// IntervalsConfig holds the per-vehicle-state telemetry reporting intervals.
type IntervalsConfig struct {
	Driving          string `yaml:"driving"`
	Standby          string `yaml:"standby"`
	StandbyNoBattery string `yaml:"standby_no_battery"`
	Hibernate        string `yaml:"hibernate"`
}

// NTPConfig controls startup time synchronisation.
type NTPConfig struct {
	Enabled *bool  `yaml:"enabled"`
	Server  string `yaml:"server"`
}

// IsEnabled reports whether NTP sync is enabled (default true).
func (n *NTPConfig) IsEnabled() bool {
	return n.Enabled == nil || *n.Enabled
}

// CommandConfig holds per-command overrides: whether the command is disabled
// and any default parameters for it.
type CommandConfig struct {
	Disabled bool           `yaml:"disabled"`
	Params   map[string]any `yaml:"params"`
}

// EventsConfig groups event-detection tunables.
type EventsConfig struct {
	Movement MovementConfig `yaml:"movement"`
}

// MovementConfig tunes the unauthorized-movement detector.
type MovementConfig struct {
	Enabled          *bool   `yaml:"enabled"`
	ArmThresholdM    float64 `yaml:"arm_threshold_m"`
	ConfirmThresholdM float64 `yaml:"confirm_threshold_m"`
	SampleCount      int     `yaml:"sample_count"`
	SampleInterval   string  `yaml:"sample_interval"`
	Cooldown         string  `yaml:"cooldown"`
	ImplausibleJumpM float64 `yaml:"implausible_jump_m"`
}

// NotificationsConfig groups the notification channels and rules.
type NotificationsConfig struct {
	Telegram TelegramConfig     `yaml:"telegram"`
	SMS      SMSConfig          `yaml:"sms"`
	Rules    []NotificationRule `yaml:"rules"`
}

// TelegramConfig configures the Telegram notification channel.
type TelegramConfig struct {
	Enabled    bool            `yaml:"enabled"`
	BotToken   string          `yaml:"bot_token"`
	ChatID     string          `yaml:"chat_id"`
	RateLimit  string          `yaml:"rate_limit"`
	QueueSize  int             `yaml:"queue_size"`
	DailyLimit int             `yaml:"daily_limit"`
	Events     map[string]bool `yaml:"events"`
}

// SMSConfig configures the SMS notification channel (via ModemManager).
type SMSConfig struct {
	Enabled     bool   `yaml:"enabled"`
	PhoneNumber string `yaml:"phone_number"`
	RateLimit   string `yaml:"rate_limit"`
	QueueSize   int    `yaml:"queue_size"`
	DailyLimit  int    `yaml:"daily_limit"`
}

// NotificationRule is a single condition→channel routing rule.
type NotificationRule struct {
	Name       string          `yaml:"name"`
	Conditions []RuleCondition `yaml:"conditions"`
	Channels   []string        `yaml:"channels"`
	Cooldown   string          `yaml:"cooldown"`
	Message    string          `yaml:"message"`
}

// RuleCondition is one predicate within a NotificationRule.
type RuleCondition struct {
	Source   string `yaml:"source"`
	Field    string `yaml:"field"`
	Operator string `yaml:"operator"`
	Value    string `yaml:"value"`
	Message  string `yaml:"message,omitempty"`
}

// GetKeepaliveInterval parses and returns the keepalive interval
func (c *UplinkConfig) GetKeepaliveInterval() time.Duration {
	return parseDurationOr(c.KeepaliveInterval, 5*time.Minute)
}

// GetReconnectMaxDelay parses and returns the max reconnect delay
func (c *UplinkConfig) GetReconnectMaxDelay() time.Duration {
	return parseDurationOr(c.ReconnectMaxDelay, 5*time.Minute)
}

// GetTransmitPeriod returns how often the offline buffer drains.
func (c *TelemetryConfig) GetTransmitPeriod() time.Duration {
	return parseDurationOr(c.TransmitPeriod, 5*time.Minute)
}

// GetRetryInterval returns the base delay between buffer-drain retries.
func (c *BufferConfig) GetRetryInterval() time.Duration {
	return parseDurationOr(c.RetryInterval, time.Minute)
}

// Interval returns the reporting interval for a cloud-facing vehicle state,
// taking main-battery presence into account for the standby case.
func (c *IntervalsConfig) Interval(state string, mainBatteryPresent bool) time.Duration {
	switch state {
	case "ready-to-drive":
		return parseDurationOr(c.Driving, 30*time.Second)
	case "hibernating":
		return parseDurationOr(c.Hibernate, 24*time.Hour)
	default: // stand-by / parked / locked
		if mainBatteryPresent {
			return parseDurationOr(c.Standby, 5*time.Minute)
		}
		return parseDurationOr(c.StandbyNoBattery, 8*time.Hour)
	}
}

// MovementEnabled reports whether movement detection is on (default true).
func (c *MovementConfig) MovementEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// GetSampleInterval returns the movement confirmation sampling interval.
func (c *MovementConfig) GetSampleInterval() time.Duration {
	return parseDurationOr(c.SampleInterval, 5*time.Second)
}

// GetCooldown returns the movement re-arm cooldown.
func (c *MovementConfig) GetCooldown() time.Duration {
	return parseDurationOr(c.Cooldown, 2*time.Minute)
}

// CommandParam looks up a default parameter for a command by dotted path,
// returning def when the command or parameter is absent.
func (c *Config) CommandParam(command, path string, def any) any {
	cc, ok := c.Commands[command]
	if !ok || cc.Params == nil {
		return def
	}
	var cur any = map[string]any(cc.Params)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return def
		}
		cur, ok = m[seg]
		if !ok {
			return def
		}
	}
	return cur
}

// CommandDisabled reports whether a command has been disabled in config.
func (c *Config) CommandDisabled(command string) bool {
	cc, ok := c.Commands[command]
	return ok && cc.Disabled
}

// IsDevelopment reports whether the service is running in development mode,
// which unlocks otherwise-restricted commands (e.g. shell).
func (c *Config) IsDevelopment() bool {
	return c.Environment == "development"
}

func parseDurationOr(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// DetectServiceName best-effort determines the systemd unit name this process
// runs under, for use by the restart command. Falls back to a sane default.
func DetectServiceName() string {
	if s := os.Getenv("SYSTEMD_SERVICE_NAME"); s != "" {
		return s
	}
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			// e.g. 0::/system.slice/uplink-service.service
			if idx := strings.LastIndex(line, "/"); idx >= 0 {
				seg := line[idx+1:]
				if strings.HasSuffix(seg, ".service") {
					return seg
				}
			}
		}
	}
	return "uplink-service.service"
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

	config.SourcePath = path
	config.applyDefaults()
	return &config, nil
}

// applyDefaults fills in defaults for any unset fields.
func (config *Config) applyDefaults() {
	if config.Uplink.KeepaliveInterval == "" {
		config.Uplink.KeepaliveInterval = "5m"
	}
	if config.Uplink.ReconnectMaxDelay == "" {
		config.Uplink.ReconnectMaxDelay = "5m"
	}
	if config.Telemetry.EventBufferPath == "" {
		config.Telemetry.EventBufferPath = "/data/uplink-service/events.queue"
	}
	if config.Telemetry.EventMaxRetries == 0 {
		config.Telemetry.EventMaxRetries = 5
	}
	if config.Telemetry.TransmitPeriod == "" {
		config.Telemetry.TransmitPeriod = "5m"
	}
	if config.Telemetry.Buffer.MaxSize == 0 {
		config.Telemetry.Buffer.MaxSize = 1000
	}
	if config.Telemetry.Buffer.MaxRetries == 0 {
		config.Telemetry.Buffer.MaxRetries = 5
	}
	if config.Telemetry.Buffer.RetryInterval == "" {
		config.Telemetry.Buffer.RetryInterval = "1m"
	}
	if config.Telemetry.Buffer.PersistPath == "" {
		config.Telemetry.Buffer.PersistPath = "/data/uplink-service/telemetry-buffer.json"
	}
	if config.Environment == "" {
		config.Environment = "production"
	}
	if config.ServiceName == "" {
		config.ServiceName = DetectServiceName()
	}
	if config.NTP.Server == "" {
		config.NTP.Server = "pool.ntp.org"
	}
	if config.RedisURL == "" {
		config.RedisURL = "localhost:6379"
	}
}
