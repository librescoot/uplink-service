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

var version = "dev"

func main() {
	configPath := flag.String("config", "/data/uplink-service/uplink.yaml", "Path to configuration file")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("uplink-service %s\n", version)
		return
	}

	if os.Getenv("JOURNAL_STREAM") != "" {
		log.SetFlags(0)
	} else {
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	}
	log.Printf("Starting uplink-service %s", version)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Loaded config from %s", *configPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	redisAddr := cfg.RedisURL
	redisPort := 6379
	if strings.Contains(redisAddr, ":") {
		parts := strings.Split(redisAddr, ":")
		redisAddr = parts[0]
		if port, err := strconv.Atoi(parts[1]); err == nil {
			redisPort = port
		}
	}

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

	writeCloudStatus(false)

	clock := timeutil.NewClock()
	startClockSync(ctx, cfg, clock)

	modem := modeminfo.NewPoller(0)
	go modem.Start(ctx)

	connMgr := connection.NewManager(cfg, version)
	collector := telemetry.NewCollector(client, version, cfg, modem)
	monitor := telemetry.NewMonitor(client, collector, connMgr)
	eventDetector := telemetry.NewEventDetector(client, connMgr, monitor, cfg.Telemetry.EventBufferPath, cfg.Telemetry.EventMaxRetries)
	cmdHandler := commands.NewHandler(connMgr, client, collector, cfg)

	buffer := telemetry.NewBuffer(client, connMgr, clock, cfg.Telemetry.Buffer, cfg.Telemetry.GetTransmitPeriod())
	publisher := telemetry.NewPublisher(collector, connMgr, clock, buffer)
	monitor.SetFlusher(publisher)

	connMgr.StatusCallback = func(connected bool) {
		writeCloudStatus(connected)
		if !connected {
			publisher.ResetBaseline()
		}
	}

	monitor.SetEventFlusher(eventDetector)

	if err := connMgr.Start(ctx); err != nil {
		log.Fatalf("Failed to start connection manager: %v", err)
	}

	go func() {
		firstConnection := true
		for {
			select {
			case <-ctx.Done():
				return
			case <-connMgr.ConnectedChannel():
				if firstConnection {
					log.Println("[Main] Connection established, starting watchers...")

					go monitor.Start(ctx)
					go eventDetector.Start(ctx)
				} else {
					log.Println("[Main] Reconnected")
				}

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

				log.Println("[Main] Sending full telemetry snapshot...")
				if err := publisher.Flush(ctx, true); err != nil {
					log.Printf("[Main] Failed to publish snapshot: %v", err)
				}

				go eventDetector.FlushBufferedEvents(ctx)
				go buffer.Flush()
			}
		}
	}()

	go intervalTicker(ctx, cfg, client, connMgr, publisher)

	cmdHandler.Start(ctx)

	go statsLogger(ctx, connMgr)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("\nShutting down gracefully...")
	cancel()
	writeCloudStatus(false)

	time.Sleep(1 * time.Second)
	log.Println("Stopped.")
}

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

func telemetryInterval(cfg *config.Config, client *ipc.Client) time.Duration {
	state, _ := client.HGet("vehicle", "state")
	present := false
	if p, err := client.HGet("battery:0", "present"); err == nil && p == "true" {
		present = true
	}
	return cfg.Telemetry.Intervals.Interval(state, present)
}

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
