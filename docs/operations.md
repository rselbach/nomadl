# Operations And Storage

This guide explains how `nomadl` ingests logs, stores data, and behaves in daily
use.

## Architecture

`nomadl` is a single Go binary with three local responsibilities:

1. Talk to the Nomad HTTP API.
2. Store logs in SQLite.
3. Serve the browser UI.

No external database or web server is required.

## Ingestion Model

When ingestion is enabled, `nomadl` periodically discovers running Nomad jobs and
running allocations. For each task it can see, it:

1. Queues a backfill of recent logs.
2. Starts a live log stream.
3. Inserts log rows into SQLite.

Discovery repeats based on `--discover-interval`.

## Backfill

Backfill fetches recent bytes per task stream. The default is 256 KiB:

```sh
nomadl --backfill-bytes=262144
```

Set it to `0` to disable backfill and only ingest future logs:

```sh
nomadl --backfill-bytes=0
```

Backfill concurrency is controlled by `--backfill-workers`.

## Live Streams

Live stream concurrency is controlled by `--max-streams`.

```sh
nomadl --max-streams=16
```

Set `--max-streams=0` for unlimited streams. Be careful: too many streams can hit
Nomad connection limits or produce rate limiting.

`--stream-start-delay` spaces out stream startup to avoid hammering Nomad.

## Service Ordering

`--priority-services` moves selected services to the front of discovery and
ingestion startup:

```sh
nomadl --priority-services=api,billing,worker
```

This does not exclude other services. It only changes order.

Use `--ingest-services` or the Settings modal to restrict ingestion to a service
allowlist.

## Stream Selection

stderr is always ingested. stdout is opt-in:

```sh
nomadl --ingest-stdout
```

This default keeps ingestion lighter and matches common service logging setups
where structured logs are written to stderr.

## SQLite Store

The default database path is:

```text
<config-dir>/nomadl.db
```

The store uses a single open SQLite connection, WAL mode, and a busy timeout to
avoid local contention between ingestion and search.

Rows are inserted with defensive de-duplication. The raw payload is preserved so
JSON details and exact raw searches can work.

## Startup Reset

By default, the database is cleared at startup:

```sh
nomadl --reset-on-start=true
```

This makes `nomadl` behave like a local cache. It starts fresh, backfills recent
logs, and then follows live streams.

To keep stored logs across restarts:

```sh
nomadl --reset-on-start=false
```

## Browser State

Some UI preferences live in browser `localStorage`:

- Sidebar collapsed state and width.
- Table column widths and visibility.
- Light/dark theme.

These are per-browser and separate from `settings.json`.

## Security

The default listen address is local-only:

```text
127.0.0.1:7788
```

If you bind to a non-local address with `--addr`, anyone who can reach that port
may be able to view logs visible to your Nomad credentials. Do not expose it on a
shared network unless you understand that risk.

The SQLite database contains copied log data. Treat it as sensitive if your logs
contain secrets, tokens, customer data, or internal identifiers.

## Release Artifacts

The release workflow builds:

- Linux binaries for `amd64` and `arm64`.
- macOS binaries for `amd64` and `arm64`.
- Debian packages.
- RPM packages.
- Arch Linux packages.
- Linux AppImage.
- Checksums.
- Homebrew formula updates.

The binary embeds the UI and local HTMX asset, so release artifacts do not need a
separate `web/` directory.
