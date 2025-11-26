# Uplink Service

Scooter-side client for librescoot uplink system. Maintains persistent connection to uplink-server, sends telemetry, and receives commands.

## Features

- WebSocket connection with automatic reconnection
- Exponential backoff (1s → 2s → 4s → ... → 5min max)
- Token-based authentication
- Telemetry sending (currently dummy data, Redis integration TODO)
- Command reception and execution
- Connection statistics tracking
- Configurable keepalive intervals

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

Create `/etc/librescoot/uplink.yml` (see `configs/uplink.example.yml`):

```yaml
uplink:
  server_url: "ws://uplink.example.com:8080/ws"
  keepalive_interval: "5m"
  reconnect_max_delay: "5m"

scooter:
  identifier: "mdb-12345678"
  token: "secret-token-here"

telemetry:
  intervals:
    driving: "10s"
    standby: "1m"
    standby_no_battery: "5m"
    hibernate: "30m"
  buffer:
    enabled: true
    max_size: 1000
    persist_path: "/data/uplink-buffer.json"

redis_url: "unix:///var/run/redis/redis.sock"
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
┌───────────────┐
│  Connection   │
│   Manager     │
└───────┬───────┘
        │
   ┌────┴────┐
   │         │
┌──▼──┐  ┌──▼────┐
│ Tel │  │ Cmd   │
│Send │  │Handle │
└─────┘  └───────┘
```

## Development

### Project Structure

```
uplink-service/
├── cmd/uplink-service/  # Main application
├── internal/
│   ├── config/          # Configuration
│   ├── connection/      # Connection manager
│   ├── telemetry/       # Telemetry sender
│   ├── commands/        # Command handler
│   ├── buffer/          # Telemetry buffer (TODO)
│   └── protocol/        # Message protocol
├── configs/             # Example configurations
└── bin/                 # Built binaries
```

## TODO

- [ ] Integrate with Redis for real telemetry (like radio-gaga)
- [ ] Implement persistent telemetry buffering
- [ ] Add command response sending
- [ ] Implement actual command execution (unlock/lock/reboot)
- [ ] Add systemd service file
- [ ] Add Yocto recipe

## License

AGPL-3.0 (matches librescoot project)
