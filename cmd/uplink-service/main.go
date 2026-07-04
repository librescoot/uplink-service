package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	ipc "github.com/librescoot/redis-ipc"

	"github.com/librescoot/uplink-service/internal/commands"
	"github.com/librescoot/uplink-service/internal/config"
	"github.com/librescoot/uplink-service/internal/connection"
	"github.com/librescoot/uplink-service/internal/modeminfo"
	"github.com/librescoot/uplink-service/internal/telemetry"
	"github.com/librescoot/uplink-service/internal/timeutil"
)

var version = "dev" // Set via ldflags at build time

func main() {
	configPath := flag.String("config", "/data/uplink-service/uplink.yaml", "Path to configuration file")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("uplink-service %s\n", version)
		return
	}

	// Skip timestamps if running under systemd/journald
	if os.Getenv("JOURNAL_STREAM") != "" {
		log.SetFlags(0)
	} else {
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	}
	log.Printf("Starting uplink-service %s", version)

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Loaded config from %s", *configPath)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Parse Redis URL
	redisAddr := cfg.RedisURL
	redisPort := 6379
	if strings.Contains(redisAddr, ":") {
		parts := strings.Split(redisAddr, ":")
		redisAddr = parts[0]
		if port, err := strconv.Atoi(parts[1]); err == nil {
			redisPort = port
		}
	}

	// Initialize redis-ipc client
	client, err := ipc.New(
		ipc.WithAddress(redisAddr),
		ipc.WithPort(redisPort),
		ipc.WithCodec(ipc.StringCodec{}),
		ipc.WithOnDisconnect(func(err error) {
			if err != nil {
				log.Printf("Redis disconnected: %v", err)
			}
		}),
	)
	if err != nil {
		log.Fatalf("Failed to create Redis client: %v", err)
	}

	// Set up cloud status reporting to Redis
	internetHash := client.Hash("internet")
	writeCloudStatus := func(connected bool) {
		status := "disconnected"
		if connected {
			status = "connected"
		}
		if err := internetHash.Set("unu-cloud", status); err != nil {
			log.Printf("[Main] Failed to update internet:unu-cloud: %v", err)
		}
	}
	// Report disconnected at startup before the first connection attempt
	writeCloudStatus(false)

	// Clock: anchored to a monotonic reference; invalid until NTP succeeds.
	// A plausible-looking wall-clock year is NOT trusted (the RTC is seeded
	// with the firmware build time at first boot).
	clock := timeutil.NewClock()
	startClockSync(ctx, cfg, clock)

	// Modem identity poller (background; degrades gracefully off-target).
	modem := modeminfo.NewPoller(0)
	go modem.Start(ctx)

	// Initialize components
	connMgr := connection.NewManager(cfg, version)
	collector := telemetry.NewCollector(client, version, cfg, modem)
	monitor := telemetry.NewMonitor(client, collector, connMgr)
	eventDetector := telemetry.NewEventDetector(client, connMgr, monitor, cfg.Telemetry.EventBufferPath, cfg.Telemetry.EventMaxRetries)
	cmdHandler := commands.NewHandler(connMgr, client, collector, cfg)

	// Telemetry buffer (offline persistence) and publisher (delta engine).
	buffer := telemetry.NewBuffer(client, connMgr, clock, cfg.Telemetry.Buffer, cfg.Telemetry.GetTransmitPeriod())
	publisher := telemetry.NewPublisher(collector, connMgr, clock, buffer)
	monitor.SetFlusher(publisher)

	// On disconnect, force the next send to be a full resync.
	connMgr.StatusCallback = func(connected bool) {
		writeCloudStatus(connected)
		if !connected {
			publisher.ResetBaseline()
		}
	}

	// Wire up bidirectional flushing: monitor <-> eventDetector
	monitor.SetEventFlusher(eventDetector)

	// Start connection manager
	if err := connMgr.Start(ctx); err != nil {
		log.Fatalf("Failed to start connection manager: %v", err)
	}

	// Handle connection events - full resync on every connect/reconnect
	go func() {
		firstConnection := true
		for {
			select {
			case <-ctx.Done():
				return
			case <-connMgr.ConnectedChannel():
				if firstConnection {
					log.Println("[Main] Connection established, starting watchers...")
					// Start watchers first to avoid missing changes during state collection
					go monitor.Start(ctx)
					go eventDetector.Start(ctx)
				} else {
					log.Println("[Main] Reconnected")
				}

				// Initialize baselines on first connection only
				if firstConnection {
					state, err := collector.CollectState(ctx)
					if err != nil {
						log.Printf("[Main] Failed to collect state: %v", err)
						continue
					}
					monitor.InitializeBaseline(state)
					eventDetector.InitializeBaseline(state)
					firstConnection = false
					go buffer.StartDrainLoop(ctx)
				}

				// Send a forced full snapshot via the publisher.
				log.Println("[Main] Sending full telemetry snapshot...")
				if err := publisher.Flush(ctx, true); err != nil {
					log.Printf("[Main] Failed to publish snapshot: %v", err)
				}

				// Replay buffered events and offline telemetry.
				go eventDetector.FlushBufferedEvents(ctx)
				go buffer.Flush()
			}
		}
	}()

	// Liveness: publish on a state-based interval even with no field changes.
	go intervalTicker(ctx, cfg, client, connMgr, publisher)

	cmdHandler.Start(ctx)

	// Start stats logger
	go statsLogger(ctx, connMgr)

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("\nShutting down gracefully...")
	cancel()
	writeCloudStatus(false)

	// Give goroutines time to clean up
	time.Sleep(1 * time.Second)
	log.Println("Stopped.")
}

// startClockSync attempts an immediate NTP sync and, on failure, keeps retrying
// in the background. Clock validity is established only by a successful NTP
// query — never by a plausible-looking wall-clock value.
func startClockSync(ctx context.Context, cfg *config.Config, clock *timeutil.Clock) {
	if !cfg.NTP.IsEnabled() {
		log.Println("[Clock] NTP disabled; timestamps remain monotonic-relative until valid")
		return
	}
	server := cfg.NTP.Server
	if _, err := clock.SyncOnce(server); err == nil {
		log.Println("[Clock] Time synchronized via NTP")
		return
	}
	go func() {
		backoff := 10 * time.Second
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				if _, err := clock.SyncOnce(server); err == nil {
					log.Println("[Clock] Time synchronized via NTP (background)")
					return
				}
				if backoff < 5*time.Minute {
					backoff *= 2
				}
			}
		}
	}()
}

// intervalTicker publishes a fresh snapshot on a state-based cadence so the
// server sees liveness even when no monitored field changes.
func intervalTicker(ctx context.Context, cfg *config.Config, client *ipc.Client, connMgr *connection.Manager, publisher *telemetry.Publisher) {
	for {
		interval := telemetryInterval(cfg, client)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			if connMgr.IsConnected() {
				if err := publisher.Flush(ctx, false); err != nil {
					log.Printf("[Main] Interval flush failed: %v", err)
				}
			}
		}
	}
}

// telemetryInterval selects the reporting interval for the current vehicle
// state and main-battery presence.
func telemetryInterval(cfg *config.Config, client *ipc.Client) time.Duration {
	state, _ := client.HGet("vehicle", "state")
	present := false
	if p, err := client.HGet("battery:0", "present"); err == nil && p == "true" {
		present = true
	}
	return cfg.Telemetry.Intervals.Interval(state, present)
}

// statsLogger prints connection statistics periodically
func statsLogger(ctx context.Context, connMgr *connection.Manager) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats := connMgr.GetStats()
			connStatus := "disconnected"
			if connected, ok := stats["connected"].(bool); ok && connected {
				if auth, ok := stats["authenticated"].(bool); ok && auth {
					connStatus = "auth"
				} else {
					connStatus = "conn"
				}
			}
			bytesSent, _ := stats["bytes_sent"].(int64)
			bytesRecv, _ := stats["bytes_received"].(int64)
			log.Printf("[Stats] %s | ↑%.1fKB ↓%.1fKB | tel:%d cmd:%d | up:%s idle:%s disc:%d",
				connStatus,
				float64(bytesSent)/1024,
				float64(bytesRecv)/1024,
				stats["telemetry_sent"], stats["commands_recv"],
				stats["uptime"], stats["idle"],
				stats["disconnects"])
		}
	}
}
