# Operations and Storage

## Ingestion pipeline

On startup, and every `--discover-interval` (default 15s) after, `nomadl`
asks Nomad for running services and their allocations. For each task it:

1. Backfills up to `--backfill-bytes` of recent logs per stream, with at
   most `--backfill-workers` backfills in flight.
2. Follows the live stream, parsing each line into structured rows.

`stderr` is always ingested; add `stdout` with `--ingest-stdout`. Which
services are ingested comes from the UI settings (persisted in
`settings.json`), overridable per run with `--ingest-services`. Services
listed in `--priority-services` are started first.

Concurrent streams are capped at `--max-streams` (default 16) to stay
under Nomad API connection limits, and stream starts are spaced by
`--stream-start-delay` to avoid thundering-herd reconnects.

## Storage

Logs live in a single SQLite database (pure-Go `modernc.org/sqlite`, no
cgo), by default at `~/.config/nomadl/nomadl.db`. Timestamps are stored as
fixed-width UTC strings so they sort correctly, and lines are deduplicated
by their stable position in the source log file, so restarts and
re-backfills don't create duplicates.

The store is capped at `--max-rows` rows (default 200,000); the oldest
rows are pruned periodically. Set `--max-rows=0` to disable the cap.

By default the store is cleared on startup (`--reset-on-start=true`), so
each session starts from a fresh backfill. Pass `--reset-on-start=false`
to keep history across runs.

## Network posture

`nomadl` binds to `127.0.0.1` by default and rejects requests whose `Host`
header doesn't match a loopback bind, which blocks DNS-rebinding attacks
against the local server. If the default port is busy, it walks forward to
the next free one; an address given explicitly via `--addr` is never
moved.

The server shuts down gracefully on `SIGINT`/`SIGTERM`, closing live
streams and the database cleanly.
