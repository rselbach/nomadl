# Operations and Storage

## Ingestion pipeline

On startup, and every `--discover-interval` (default 15s) after, `nomadl`
lists Nomad's running allocations in a single API call. Each running task
gets one follower per ingested stream. A follower starts
`--backfill-bytes` before the end of the task's log, so recent history
arrives first, and then keeps following new output, parsing each line into
a structured row.

When a stream drops, its follower reconnects with backoff (1s up to 30s)
and re-reads at least 64 KiB of the log, so output written while it was
disconnected isn't lost; lines already stored are dropped by deduplication.
A followed log never ends on its own after its task stops, so discovery
closes the followers of tasks that are no longer running or whose service
left the ingest allowlist.

`stderr` is always ingested; add `stdout` with `--ingest-stdout`. Which
services are ingested comes from the UI settings (persisted in
`settings.json`), overridable per run with `--ingest-services`. Services
listed in `--priority-services` are started first.

Concurrent streams are capped at `--max-streams` (default 64) to stay
under Nomad's per-client connection limit (100 by default). Streams over
the cap wait, in priority order, and start when a slot frees up; the
Status panel lists them. Stream starts are spaced by
`--stream-start-delay` to avoid opening every connection at once. Nomad
API calls made during discovery time out after 10 seconds.

## Storage

Logs live in a single SQLite database (pure-Go `modernc.org/sqlite`, no
cgo), by default at `~/.config/nomadl/nomadl.db`. Timestamps are stored as
fixed-width UTC strings so they sort correctly, and lines are deduplicated
by their stable position in the source log file, so restarts and
re-backfills don't create duplicates.

Lines are stored in the order they appear in the task's log. A line
without a recognizable timestamp gets an estimated one, marked with `~` in
the UI: if it arrived within a second of a line that has its own
timestamp, as stack-trace lines and backfilled history do, it shares that
line's time; otherwise it gets the time it arrived.

All streams feed a single writer that commits lines in batches (at least
every 200ms). Searches run on separate read-only connections, so a slow
query never holds up ingestion and vice versa.

The store is capped at `--max-rows` rows (default 200,000); the oldest
rows are pruned periodically. Set `--max-rows=0` to disable the cap.

By default the store is cleared on startup (`--reset-on-start=true`), so
each session starts from a fresh backfill. Pass `--reset-on-start=false`
to keep history across runs.

## Network posture

`nomadl` binds to `127.0.0.1` by default and rejects requests whose `Host`
header doesn't match a loopback bind, which blocks DNS-rebinding attacks
against the local server. State-changing requests (clearing logs, saving
settings) are also rejected when a browser sends them from another site, so
a page you visit can't drive the local API. If the default port is busy, it
walks forward to the next free one; an address given explicitly via
`--addr` is never moved.

The server shuts down gracefully on `SIGINT`/`SIGTERM`, closing live
streams and the database cleanly.
