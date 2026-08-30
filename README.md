# Librescoot Uplink Service

Part of the [Librescoot](https://librescoot.org/) open-source platform.

## Overview

`uplink-service` is the scooter-side WebSocket client for Librescoot cloud connectivity. After authenticating with the configured vehicle identifier and token, it collects Redis/Valkey state, publishes snapshots, deltas, buffered telemetry, and events, accepts cloud commands, and reflects cloud connectivity in Redis.

## Capabilities

- Authenticated WebSocket connection with 1-second exponential reconnect backoff capped by configuration and configurable keepalives.
- Initial full state and subsequent priority/debounced telemetry updates from vehicle Redis/Valkey hashes.
- Persistent telemetry buffering and line-oriented event buffering for offline operation.
- Event reporting for battery, power, connectivity, locks, GPS, temperature, OTA, alarm, and Redis Stream fault changes.
- Server-issued commands for vehicle control, diagnostics, configuration, keycards, and service restart, subject to configuration and environment checks.
- NTP clock synchronization and modem metadata collection.

## Operation and interfaces

### Redis/Valkey contract

The service uses `redis_url` (default `localhost:6379`) and collects state from these hashes:

```text
vehicle, battery:0, battery:1, aux-battery, cb-battery, engine-ecu,
power-manager, power-mux, internet, modem, gps, keycard, ble, dashboard,
system, ota, alarm, navigation, scooter
```

It watches a subset for telemetry changes and event detection. On startup and connection it writes `internet.unu-cloud=disconnected` or `connected` to show WebSocket authentication status. The collector adds `meta.build-version`, `meta.environment`, and `meta.identifier`; it also adds an MDB board serial and modem fields when available. Vehicle states `hop-on` and `hop-on-learning` are translated to `stand-by` and `parked` respectively in cloud telemetry.

Remote commands are translated into Redis queue requests. Common mappings are:

| Remote command | Redis queue and value |
|---|---|
| `unlock`, `lock`, `lock_hibernate`, `force_lock` | `scooter:state`: `unlock`, `lock`, `lock-hibernate`, `force-lock` |
| `open_seatbox` | `scooter:seatbox`: `open` |
| `honk` | `scooter:horn`: `on`, then `off` after required `duration` milliseconds |
| `blinker_left`, `blinker_right`, `blinker_both`, `blinker_off` | `scooter:blinker`: `left`, `right`, `both`, `off` |
| `dashboard_on`, `dashboard_off`, `engine_on`, `engine_off`, `handlebar_lock`, `handlebar_unlock` | `scooter:hardware` with the corresponding `name:on`, `name:off`, `handlebar:lock`, or `handlebar:unlock` value |
| `reboot`, `hibernate`, `hibernate_manual` | `scooter:power`: `reboot`, `hibernate`, `hibernate-manual` |
| `alarm_arm`, `alarm_disarm`, `alarm_enable`, `alarm_disable`, `alarm_stop` | `scooter:alarm`: `arm`, `disarm`, `enable`, `disable`, `stop` |

It also handles `locate`, `alarm`, `navigate`, `redis`, `config:get`, `config:set`, `config:del`, `config:save`, `keycards:list`, `keycards:add`, `keycards:delete`, `keycards:master_key:get`, `keycards:master_key:set`, `restart`, `get_state`, and `ping`. Every accepted command receives a `command_response` carrying the original request ID and a `success` or `failed` status.

### WebSocket protocol

Client messages use JSON with RFC3339 UTC timestamps: `auth`, `state`, `change`, `telemetry_delta`, `telemetry_batch`, `event`, `keepalive`, and `command_response`. The authentication payload includes `identifier`, `token`, client version, and protocol version `0`. Server messages are `auth_response`, `command`, `config_update`, and `keepalive`.

The first successful connection initializes telemetry/event baselines, sends a full state snapshot, and starts the offline telemetry drain. Disconnecting resets the telemetry baseline so the next connection receives a fresh full snapshot. `config_update` deltas are applied to the loaded YAML file; a message with `restart: true` requests a service restart.

## Configuration

The program accepts:

```text
uplink-service [-config PATH] [-version]
```

The default configuration path is `/data/uplink-service/uplink.yaml`. Start with [`configs/uplink.example.yml`](configs/uplink.example.yml), which documents the available YAML structure. At minimum set the cloud URL and credentials:

```yaml
uplink:
  server_url: "wss://uplink.example.invalid/ws"

scooter:
  identifier: "VEHICLE_IDENTIFIER"
  token: "REPLACE_WITH_SECRET"

redis_url: "localhost:6379"
```

Unset values receive these relevant defaults: five-minute keepalive, five-minute reconnect cap, `/data/uplink-service/events.queue`, event retry limit 5, five-minute transmit period, telemetry buffer maximum 1000 snapshots, telemetry-buffer retry limit 5, one-minute buffer retry interval, `/data/uplink-service/telemetry-buffer.json`, production environment, `pool.ntp.org`, and local Redis/Valkey.

`telemetry.intervals` controls periodic snapshot cadence. The defaults are 30 seconds in `ready-to-drive`, five minutes in other states with a main battery, eight hours without one, and 24 hours while `hibernating`.

The `commands` map can disable individual commands or define default parameters. The `shell` command is rejected unless `environment: development`; use production for deployed vehicles. `service_name` controls the unit targeted by restart handling and is auto-detected when omitted.

## Build and test

Requires Go and the dependencies declared in `go.mod`.

```sh
make build       # static Linux ARMv7 binary: bin/uplink-service
make build-host  # host binary: bin/uplink-service
make test
make lint        # requires golangci-lint
```

`make run` builds for the host and uses `configs/uplink.example.yml`. `make fmt`, `make deps`, and `make clean` are also available.

## Deployment and runtime dependencies

The Yocto package installs `/usr/bin/uplink-service`, `librescoot-uplink.service`, and tmpfiles rule `/etc/tmpfiles.d/uplink-service.conf`. The rule creates `/data/uplink-service` as `root:root` mode `0755`. The unit requires `valkey.service`, starts after the network and Valkey, requires `/data/uplink-service/uplink.yaml` to exist, runs with that directory as its working directory, and executes:

```text
/usr/bin/uplink-service -config /data/uplink-service/uplink.yaml
```

Runtime dependencies are a reachable WebSocket server, Redis/Valkey, vehicle services that consume the command queues, and optional NTP and modem information sources. The service updates `internet.unu-cloud` even while the cloud connection is unavailable.

## Operational and security notes

- The configuration file contains an authentication token. Keep `/data/uplink-service/uplink.yaml`, event queue, and telemetry buffer readable and writable only by trusted administrators and the service account; the shipped tmpfiles directory mode alone does not protect files created within it.
- Use `wss://` for cloud endpoints. The client supports the configured URL directly and does not add transport security or certificate policy beyond the WebSocket/TLS stack.
- Remote commands can control locks, power, horn, blinkers, and configuration. Keep the server, token issuance, Redis/Valkey access, and command configuration under strict administrative control. Leave `environment: production` and the `redis` debug command disabled unless there is a controlled maintenance need.
- Offline event and telemetry buffers are persistent local records. Monitor available `/data` space and protect their contents as vehicle operational data.
- A command response reports local dispatch success or failure; it does not prove that the downstream service completed the requested action.

## License

This project is licensed under the [GNU Affero General Public License v3.0](LICENSE).

Made with ❤️ by the Librescoot community
