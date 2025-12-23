# Uplink Service

Scooter-side client for librescoot uplink system. Maintains persistent connection to uplink-server, sends telemetry, and receives commands.

## Features

- WebSocket connection with automatic reconnection
- Exponential backoff (1s → 2s → 4s → ... → 5min max)
- Token-based authentication (VIN + token)
- Real-time telemetry from Redis via redis-ipc library
  - Nested object format for state and change messages
  - Baseline initialization to prevent duplicate change notifications
  - Configurable debounce to batch rapid changes
- Event detection with persistent buffering and retry logic
  - Battery critical monitoring (traction batteries + control board battery)
  - Power state changes and NRF reset tracking
  - GPS fix status changes
  - Connectivity status monitoring
  - Temperature monitoring (batteries, ECU)
  - Fault stream monitoring (events:faults)
  - Exponential backoff retry (configurable max retries)
- Command reception and execution (lock/unlock, power, hardware control, etc.)
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

## Commands

The uplink service supports these commands from the server:

### State Commands
| Command | Queue | Description |
|---------|-------|-------------|
| `unlock` | `scooter:state` | Unlocks the vehicle |
| `lock` | `scooter:state` | Locks the vehicle |
| `lock_hibernate` | `scooter:state` | Locks and prepares for hibernation |
| `force_lock` | `scooter:state` | Forces lock regardless of state |

### Seatbox Commands
| Command | Queue | Description |
|---------|-------|-------------|
| `open_seatbox` | `scooter:seatbox` | Opens seatbox lock |

### Horn & Blinker Commands
| Command | Queue | Description |
|---------|-------|-------------|
| `honk` | `scooter:horn` | Activates horn (duration in params) |
| `blinker_left` | `scooter:blinker` | Activates left blinker |
| `blinker_right` | `scooter:blinker` | Activates right blinker |
| `blinker_both` | `scooter:blinker` | Activates hazard lights |
| `blinker_off` | `scooter:blinker` | Turns off blinkers |

### Hardware Commands
| Command | Queue | Description |
|---------|-------|-------------|
| `dashboard_on` | `scooter:hardware` | Powers on dashboard |
| `dashboard_off` | `scooter:hardware` | Powers off dashboard |
| `engine_on` | `scooter:hardware` | Enables engine |
| `engine_off` | `scooter:hardware` | Disables engine |
| `handlebar_lock` | `scooter:hardware` | Engages handlebar lock |
| `handlebar_unlock` | `scooter:hardware` | Releases handlebar lock |

### Power Commands
| Command | Queue | Description |
|---------|-------|-------------|
| `reboot` | `scooter:power` | Initiates system reboot |
| `hibernate` | `scooter:power` | Enters automatic hibernation |
| `hibernate_manual` | `scooter:power` | Enters manual hibernation mode |

### Special Commands
| Command | Queue | Description |
|---------|-------|-------------|
| `get_state` | - | Sends full state snapshot |
| `ping` | - | No-op for testing connectivity |

All commands return a `command_response` message with status "success" or "failed".

## Protocol

### Client → Server Messages

- **auth**: Authenticate on connection
- **state**: Full telemetry state snapshot (on connect)
- **change**: Delta updates (field-level changes)
- **event**: Critical events (battery critical, connectivity lost, etc.)
- **keepalive**: Keepalive ping
- **command_response**: Response to server command

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
    │  8 Hash      │
    │  Watchers    │
    │  + Fault     │
    │  Stream      │
    └──────────────┘
          │
    ┌─────▼────────┐
    │ Redis (IPC)  │
    │ vehicle, gps │
    │ battery, etc │
    │ events:fault │
    └──────────────┘
```

**Components:**
- **Monitor**: Watches 12 Redis hashes, debounces changes, sends delta updates to server
- **EventDetector**: Watches 8 Redis hashes + fault stream for critical events
  - Battery monitoring (traction + CB battery)
  - Power state and NRF reset tracking
  - GPS, connectivity, temperature monitoring
  - Fault stream consumer (events:faults)
  - Persistent buffering with exponential backoff retry
- **CommandHandler**: Receives and executes commands via Redis IPC (state, hardware, power, etc.)
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

## Deployment

A Yocto/BitBake recipe is available at:
```
meta-librescoot/recipes-core/uplink-service/uplink-service.bb
```

The service is deployed as a systemd unit (`librescoot-uplink.service`) and expects configuration at `/data/uplink.yml`.

## License

[AGPL-3.0](LICENSE)
