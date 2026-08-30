package telemetry

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	ipc "github.com/librescoot/redis-ipc"

	"github.com/librescoot/uplink-service/internal/config"
	"github.com/librescoot/uplink-service/internal/protocol"
	"github.com/librescoot/uplink-service/internal/timeutil"
)

// No TTL: buffered telemetry is cleared only after a confirmed batch send.
const bufferRedisKey = "uplink-service:telemetry-buffer"

type BatchSink interface {
	IsConnected() bool
	SendTelemetryBatch(snapshots []protocol.TelemetrySnapshot) error
}

type bufferedSnapshot struct {
	Data      map[string]any `json:"data"`
	Timestamp string         `json:"timestamp"`
	Session   string         `json:"session"`
}

type Buffer struct {
	client        *ipc.Client
	sink          BatchSink
	clock         *timeutil.Clock
	cfg           config.BufferConfig
	drainInterval time.Duration

	mu    sync.Mutex
	items []bufferedSnapshot
}

func NewBuffer(client *ipc.Client, sink BatchSink, clock *timeutil.Clock, cfg config.BufferConfig, drainInterval time.Duration) *Buffer {
	if drainInterval <= 0 {
		drainInterval = 5 * time.Minute
	}
	b := &Buffer{client: client, sink: sink, clock: clock, cfg: cfg, drainInterval: drainInterval}
	b.load()
	return b
}

func (b *Buffer) Add(snapshot map[string]any, timestamp string) {
	if !b.cfg.Enabled {
		return
	}
	b.mu.Lock()
	b.items = append(b.items, bufferedSnapshot{
		Data:      snapshot,
		Timestamp: timestamp,
		Session:   b.clock.SessionID(),
	})
	if b.cfg.MaxSize > 0 && len(b.items) > b.cfg.MaxSize {
		b.items = subsample(b.items)
	}
	b.persistLocked()
	b.mu.Unlock()
}

func (b *Buffer) StartDrainLoop(ctx context.Context) {
	if !b.cfg.Enabled {
		return
	}
	ticker := time.NewTicker(b.drainInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.Flush()
		}
	}
}

// Flush retains snapshots on send failure. Relative times cannot cross a process
// session because their monotonic anchor no longer exists.
func (b *Buffer) Flush() {
	if !b.cfg.Enabled {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.items) == 0 || !b.sink.IsConnected() {
		return
	}

	snapshots := make([]protocol.TelemetrySnapshot, 0, len(b.items))
	for _, it := range b.items {
		ts := it.Timestamp
		if timeutil.IsRelative(ts) {
			// Relative timestamps cannot be reprojected across process sessions.
			if it.Session != b.clock.SessionID() {
				continue
			}
			if b.clock.Valid() {
				if reproj, ok := b.clock.Reproject(ts); ok {
					ts = reproj
				}
			}
		}
		snapshots = append(snapshots, protocol.TelemetrySnapshot{Data: it.Data, Timestamp: ts})
	}

	if len(snapshots) == 0 {
		b.items = nil
		b.persistLocked()
		return
	}

	if err := b.sink.SendTelemetryBatch(snapshots); err != nil {
		log.Printf("[Buffer] Batch send failed, retaining %d snapshots: %v", len(b.items), err)
		return
	}

	log.Printf("[Buffer] Flushed %d snapshots", len(snapshots))
	b.items = nil
	b.persistLocked()
}

// subsample retains both endpoints so an offline period's extent is preserved.
func subsample(items []bufferedSnapshot) []bufferedSnapshot {
	if len(items) < 3 {
		return items
	}
	out := make([]bufferedSnapshot, 0, len(items)/2+2)
	out = append(out, items[0])
	for i := 1; i < len(items)-1; i += 2 {
		out = append(out, items[i])
	}
	out = append(out, items[len(items)-1])
	return out
}

func (b *Buffer) persistLocked() {
	data, err := json.Marshal(b.items)
	if err != nil {
		log.Printf("[Buffer] Marshal failed: %v", err)
		return
	}
	if b.client != nil {

		if err := b.client.Set(bufferRedisKey, string(data), 0); err != nil {
			log.Printf("[Buffer] Redis persist failed: %v", err)
		}
	}
	if b.cfg.PersistPath != "" {
		// Keep a disk copy when Redis is unavailable or wiped.
		dir := filepath.Dir(b.cfg.PersistPath)
		_ = os.MkdirAll(dir, 0o755)
		tmp := b.cfg.PersistPath + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err == nil {
			_ = os.Rename(tmp, b.cfg.PersistPath)
		}
	}
}

// Prefer Redis, but retain a disk fallback for Redis loss or service restarts.
func (b *Buffer) load() {

	if b.client != nil {
		if s, err := b.client.Get(bufferRedisKey); err == nil && s != "" {
			if b.unmarshalInto(s) {
				return
			}
		}
	}
	if b.cfg.PersistPath != "" {
		if data, err := os.ReadFile(b.cfg.PersistPath); err == nil {
			b.unmarshalInto(string(data))
		}
	}
}

func (b *Buffer) unmarshalInto(s string) bool {
	var items []bufferedSnapshot
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		return false
	}
	b.items = items
	if len(items) > 0 {
		log.Printf("[Buffer] Loaded %d buffered snapshots", len(items))
	}
	return true
}
