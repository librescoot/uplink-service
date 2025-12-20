# Uplink Service

Scooter-side client for librescoot uplink system. Maintains persistent connection to uplink-server, sends telemetry, and receives commands.

## Features

- WebSocket connection with automatic reconnection
- Exponential backoff (1s → 2s → 4s → ... → 5min max)
- Token-based authentication (VIN + token)
- Real-time telemetry from Redis via redis-ipc library
- Event detection with persistent buffering (battery critical, power state changes, GPS, etc.)
- Command reception and execution
- Connection statistics tracking
- Configurable keepalive and debounce intervals

## Building

```bash
# Download dependencies
make deps

# Build service
make build

# Build for specific platforms
make client-linux-amd64
make client-linux-arm
```

## Configuration

Create `/data/uplink.yml` (see `configs/uplink.example.yml`):

```yaml
uplink:
  server_url: "ws://uplink.example.com:8080/ws"
  keepalive_interval: "5m"
  reconnect_max_delay: "5m"

scooter:
  identifier: "WUNU2S3B7MZ000147"  # Vehicle VIN
  token: "your-auth-token-here"

telemetry:
  debounce_duration: "1s"          # Debounce interval for telemetry updates
  event_buffer_path: "/data/uplink-events.queue"
  event_max_retries: 10

redis_url: "localhost:6379"
```

## Running

```bash
./bin/uplink-service -config /etc/librescoot/uplink.yml
```

## Protocol

### Client → Server Messages

- **auth**: Authenticate on connection
- **telemetry**: Send telemetry data
- **keepalive**: Keepalive ping
- **command_response**: Response to server command (TODO)

### Server → Client Messages

- **auth_response**: Authentication result
- **command**: Execute command on scooter
- **keepalive**: Keepalive ping

## Architecture

```
┌─────────────────────────────────────────┐
│           Connection Manager            │
│  (WebSocket, Auth, Reconnection Logic)  │
└────────┬────────────────────────┬────────┘
         │                        │
    ┌────▼────────┐         ┌─────▼────────┐
    │  Telemetry  │         │   Command    │
    │   Monitor   │         │   Handler    │
    │             │         │              │
    │ 12 Hash     │         └──────────────┘
    │ Watchers    │
    └─────┬───────┘
          │
    ┌─────▼────────┐
    │    Event     │
    │  Detector    │
    │              │
    │  7 Hash      │
    │  Watchers    │
    └──────────────┘
          │
    ┌─────▼────────┐
    │ Redis (IPC)  │
    │ vehicle, gps │
    │ battery, etc │
    └──────────────┘
```

**Components:**
- **Monitor**: Watches 12 Redis hashes, debounces changes, sends telemetry updates
- **EventDetector**: Watches for critical events (battery low, power state changes, GPS fix, etc.)
- **HashWatcher**: redis-ipc abstraction for PUBSUB + HGET with automatic debouncing

## Development

### Project Structure

```
uplink-service/
├── cmd/uplink-service/     # Main application
├── internal/
│   ├── config/             # Configuration
│   ├── connection/         # Connection manager
│   ├── telemetry/          # Monitor, EventDetector, Collector
│   ├── commands/           # Command handler
│   └── protocol/           # Message protocol
├── configs/                # Example configurations
├── librescoot-uplink.service  # Systemd service file
└── bin/                    # Built binaries (not in git)
```

## TODO

- [ ] Add command response sending
- [ ] Implement actual command execution (unlock/lock/reboot)
- [ ] Add Yocto recipe
- [ ] Add integration tests

## License

AGPL-3.0 (matches librescoot project)
