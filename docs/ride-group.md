# Live ride-group transport

Live sharing is opt-in: `ride_group.enabled: true` in the service configuration. The default is disabled. The adapter announces `ride_group_v2` only after authentication and never downgrades to v1.

The dashboard owns the durable stop journal. The service handles transient `ride-group:activity` and `ride-group:control` messages and reads existing GPS producer samples; it creates no location history or journal, changes no telemetry cadence, and performs no vehicle actuation.

Prepared stops reconcile with a read-only query before a fresh, correlated dashboard probe permits the same-fence apply. Recovered uncertain states cannot authorize new stop applications. Settings and IPC recovery require fresh journal reconciliation.

The WebSocket writer maintains one latest frame per live message family, caps queue residence at one second and writes at 500 ms or the remaining frame lifetime, whichever is shorter. It checks authorization again at the socket boundary and pins every frame to its authenticated connection. Ordinary messages and live frames use the same serialized writer.

Validation includes all 24 v1 and 60 v2 shared fixtures, stop/recovery and source-freshness regressions, actual local WebSocket delivery, final-boundary denial, queue replacement, expiry, connection retirement, race tests and ARMv7 builds. Socket tests verify application boundaries, not device or cellular latency.
