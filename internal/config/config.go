package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Uplink        UplinkConfig             `yaml:"uplink"`
	Scooter       ScooterConfig            `yaml:"scooter"`
	Telemetry     TelemetryConfig          `yaml:"telemetry"`
	Events        EventsConfig             `yaml:"events"`
	Notifications NotificationsConfig      `yaml:"notifications"`
	Commands      map[string]CommandConfig `yaml:"commands"`
	NTP           NTPConfig                `yaml:"ntp"`
	Environment   string                   `yaml:"environment"`
	ServiceName   string                   `yaml:"service_name"`
	RedisURL      string                   `yaml:"redis_url"`

	SourcePath string `yaml:"-"`
}

type UplinkConfig struct {
	ServerURL         string `yaml:"server_url"`
	FallbackURL       string `yaml:"fallback_url,omitempty"`
	KeepaliveInterval string `yaml:"keepalive_interval"`
	ReconnectMaxDelay string `yaml:"reconnect_max_delay"`
}

type ScooterConfig struct {
	Identifier string `yaml:"identifier"`
	Token      string `yaml:"token"`
	Name       string `yaml:"name,omitempty"`
}

type TelemetryConfig struct {
	EventBufferPath string          `yaml:"event_buffer_path"`
	EventMaxRetries int             `yaml:"event_max_retries"`
	TransmitPeriod  string          `yaml:"transmit_period"`
	Buffer          BufferConfig    `yaml:"buffer"`
	Intervals       IntervalsConfig `yaml:"intervals"`
}

type BufferConfig struct {
	Enabled       bool   `yaml:"enabled"`
	MaxSize       int    `yaml:"max_size"`
	MaxRetries    int    `yaml:"max_retries"`
	RetryInterval string `yaml:"retry_interval"`
	PersistPath   string `yaml:"persist_path"`
}

type IntervalsConfig struct {
	Driving          string `yaml:"driving"`
	Standby          string `yaml:"standby"`
	StandbyNoBattery string `yaml:"standby_no_battery"`
	Hibernate        string `yaml:"hibernate"`
}

type NTPConfig struct {
	Enabled *bool  `yaml:"enabled"`
	Server  string `yaml:"server"`
}

func (n *NTPConfig) IsEnabled() bool {
	return n.Enabled == nil || *n.Enabled
}

type CommandConfig struct {
	Disabled bool           `yaml:"disabled"`
	Params   map[string]any `yaml:"params"`
}

type EventsConfig struct {
	Movement MovementConfig `yaml:"movement"`
}

type MovementConfig struct {
	Enabled           *bool   `yaml:"enabled"`
	ArmThresholdM     float64 `yaml:"arm_threshold_m"`
	ConfirmThresholdM float64 `yaml:"confirm_threshold_m"`
	SampleCount       int     `yaml:"sample_count"`
	SampleInterval    string  `yaml:"sample_interval"`
	Cooldown          string  `yaml:"cooldown"`
	ImplausibleJumpM  float64 `yaml:"implausible_jump_m"`
}

type NotificationsConfig struct {
	Telegram TelegramConfig     `yaml:"telegram"`
	SMS      SMSConfig          `yaml:"sms"`
	Rules    []NotificationRule `yaml:"rules"`
}

type TelegramConfig struct {
	Enabled    bool            `yaml:"enabled"`
	BotToken   string          `yaml:"bot_token"`
	ChatID     string          `yaml:"chat_id"`
	RateLimit  string          `yaml:"rate_limit"`
	QueueSize  int             `yaml:"queue_size"`
	DailyLimit int             `yaml:"daily_limit"`
	Events     map[string]bool `yaml:"events"`
}

type SMSConfig struct {
	Enabled     bool   `yaml:"enabled"`
	PhoneNumber string `yaml:"phone_number"`
	RateLimit   string `yaml:"rate_limit"`
	QueueSize   int    `yaml:"queue_size"`
	DailyLimit  int    `yaml:"daily_limit"`
}

type NotificationRule struct {
	Name       string          `yaml:"name"`
	Conditions []RuleCondition `yaml:"conditions"`
	Channels   []string        `yaml:"channels"`
	Cooldown   string          `yaml:"cooldown"`
	Message    string          `yaml:"message"`
}

type RuleCondition struct {
	Source   string `yaml:"source"`
	Field    string `yaml:"field"`
	Operator string `yaml:"operator"`
	Value    string `yaml:"value"`
	Message  string `yaml:"message,omitempty"`
}

func (c *UplinkConfig) GetKeepaliveInterval() time.Duration {
	return parseDurationOr(c.KeepaliveInterval, 5*time.Minute)
}

func (c *UplinkConfig) GetReconnectMaxDelay() time.Duration {
	return parseDurationOr(c.ReconnectMaxDelay, 5*time.Minute)
}

func (c *TelemetryConfig) GetTransmitPeriod() time.Duration {
	return parseDurationOr(c.TransmitPeriod, 5*time.Minute)
}

func (c *BufferConfig) GetRetryInterval() time.Duration {
	return parseDurationOr(c.RetryInterval, time.Minute)
}

func (c *IntervalsConfig) Interval(state string, mainBatteryPresent bool) time.Duration {
	switch state {
	case "ready-to-drive":
		return parseDurationOr(c.Driving, 30*time.Second)
	case "hibernating":
		return parseDurationOr(c.Hibernate, 24*time.Hour)
	default:
		if mainBatteryPresent {
			return parseDurationOr(c.Standby, 5*time.Minute)
		}
		return parseDurationOr(c.StandbyNoBattery, 8*time.Hour)
	}
}

func (c *MovementConfig) MovementEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c *MovementConfig) GetSampleInterval() time.Duration {
	return parseDurationOr(c.SampleInterval, 5*time.Second)
}

func (c *MovementConfig) GetCooldown() time.Duration {
	return parseDurationOr(c.Cooldown, 2*time.Minute)
}

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

func (c *Config) CommandDisabled(command string) bool {
	cc, ok := c.Commands[command]
	return ok && cc.Disabled
}

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

func DetectServiceName() string {
	if s := os.Getenv("SYSTEMD_SERVICE_NAME"); s != "" {
		return s
	}
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {

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
