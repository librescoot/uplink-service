package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/librescoot/uplink-service/internal/commands"
	"github.com/librescoot/uplink-service/internal/config"
	"github.com/librescoot/uplink-service/internal/connection"
	"github.com/librescoot/uplink-service/internal/telemetry"
)

const version = "1.0.0"

func main() {
	configPath := flag.String("config", "/etc/librescoot/uplink.yml", "Path to configuration file")
	flag.Parse()

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Printf("Starting uplink-service v%s", version)

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Loaded config from %s", *configPath)
	log.Printf("Server: %s", cfg.Uplink.ServerURL)
	log.Printf("Identifier: %s", cfg.Scooter.Identifier)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize Redis client
	var redisOpts *redis.Options
	if len(cfg.RedisURL) > 7 && cfg.RedisURL[:7] == "unix://" {
		redisOpts = &redis.Options{
			Network: "unix",
			Addr:    cfg.RedisURL[7:],
		}
	} else if len(cfg.RedisURL) > 6 && cfg.RedisURL[:6] == "tcp://" {
		redisOpts = &redis.Options{
			Network: "tcp",
			Addr:    cfg.RedisURL[6:],
		}
	} else {
		redisOpts = &redis.Options{
			Addr: cfg.RedisURL,
		}
	}
	redisClient := redis.NewClient(redisOpts)

	// Test Redis connection
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Printf("Warning: Redis connection failed: %v (will retry)", err)
	} else {
		log.Println("Connected to Redis")
	}

	// Initialize components
	connMgr := connection.NewManager(cfg)
	collector := telemetry.NewCollector(redisClient)
	monitor := telemetry.NewMonitor(redisClient, collector, connMgr, cfg.Telemetry.GetDebounceDuration())
	eventDetector := telemetry.NewEventDetector(redisClient, connMgr, cfg.Telemetry.EventBufferPath, cfg.Telemetry.EventMaxRetries)
	cmdHandler := commands.NewHandler(connMgr)

	// Start connection manager
	if err := connMgr.Start(ctx); err != nil {
		log.Fatalf("Failed to start connection manager: %v", err)
	}

	// Wait for connection, then send initial state snapshot
	go func() {
		for !connMgr.IsConnected() {
			time.Sleep(1 * time.Second)
		}

		log.Println("[Main] Connection established, sending initial state snapshot...")
		state, err := collector.CollectState(ctx)
		if err != nil {
			log.Printf("[Main] Failed to collect initial state: %v", err)
		} else {
			if err := connMgr.SendState(state); err != nil {
				log.Printf("[Main] Failed to send initial state: %v", err)
			}
		}

		// Start monitoring components
		go monitor.Start(ctx)
		go eventDetector.Start(ctx)
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
