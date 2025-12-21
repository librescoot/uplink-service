package main

import (
	"context"
	"flag"
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
	"github.com/librescoot/uplink-service/internal/telemetry"
)

var version = "dev" // Set via ldflags at build time

func main() {
	configPath := flag.String("config", "/etc/librescoot/uplink.yml", "Path to configuration file")
	flag.Parse()

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

	// Initialize components
	connMgr := connection.NewManager(cfg, version)
	collector := telemetry.NewCollector(client)
	monitor := telemetry.NewMonitor(client, collector, connMgr, cfg.Telemetry.GetDebounceDuration())
	eventDetector := telemetry.NewEventDetector(client, connMgr, cfg.Telemetry.EventBufferPath, cfg.Telemetry.EventMaxRetries)
	cmdHandler := commands.NewHandler(connMgr, client)

	// Start connection manager
	if err := connMgr.Start(ctx); err != nil {
		log.Fatalf("Failed to start connection manager: %v", err)
	}

	// Wait for connection, start watchers, then collect state and initialize baselines
	go func() {
		for !connMgr.IsConnected() {
			time.Sleep(1 * time.Second)
		}

		// Start watchers first to avoid missing changes during state collection
		log.Println("[Main] Connection established, starting watchers...")
		go monitor.Start(ctx)
		go eventDetector.Start(ctx)

		// Collect state snapshot
		log.Println("[Main] Collecting state snapshot...")
		state, err := collector.CollectState(ctx)
		if err != nil {
			log.Printf("[Main] Failed to collect initial state: %v", err)
			return
		}

		// Initialize change trackers with current state to filter duplicates
		monitor.InitializeBaseline(state)
		eventDetector.InitializeBaseline(state)

		// Send state snapshot to server
		log.Println("[Main] Sending initial state snapshot...")
		if err := connMgr.SendState(state); err != nil {
			log.Printf("[Main] Failed to send initial state: %v", err)
		}
	}()

	cmdHandler.Start(ctx)

	// Start stats logger
	go statsLogger(ctx, connMgr)

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("\nShutting down gracefully...")
	cancel()

	// Give goroutines time to clean up
	time.Sleep(1 * time.Second)
	log.Println("Stopped.")
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
			if stats["connected"].(bool) {
				if stats["authenticated"].(bool) {
					connStatus = "auth"
				} else {
					connStatus = "conn"
				}
			}
			log.Printf("[Stats] %s | ↑%.1fKB ↓%.1fKB | tel:%d cmd:%d | up:%s idle:%s disc:%d",
				connStatus,
				float64(stats["bytes_sent"].(int64))/1024,
				float64(stats["bytes_received"].(int64))/1024,
				stats["telemetry_sent"], stats["commands_recv"],
				stats["uptime"], stats["idle"],
				stats["disconnects"])
		}
	}
}
