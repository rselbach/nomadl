# nomadl Documentation

`nomadl` is a local, single-binary Nomad log explorer. It ingests HashiCorp
Nomad allocation logs into a local SQLite database and serves a browser UI
for searching, filtering, inspecting, and graphing them.

## Guides

- [Getting started](getting-started.md) — install, run, and connect to a
  Nomad cluster.
- [Configuration](configuration.md) — command-line flags, environment
  variables, and persisted settings.
- [Query syntax](query-syntax.md) — the search language: terms, operators,
  field filters, ranges, and JSON attributes.
- [Using the UI](using-the-ui.md) — the histogram, live tail, details
  drawer, and keyboard shortcuts.
- [Operations and storage](operations.md) — how ingestion, the SQLite
  store, and pruning work.
- [Development and releases](development.md) — building, testing, and
  cutting releases.

## How it works

`nomadl` runs a small HTTP server bound to loopback by default. On startup
it discovers running Nomad services, backfills a slice of each task's recent
logs, and then follows the live streams, parsing each line into structured
rows (timestamp, level, message, raw payload) stored in SQLite. The web UI
queries that store, so searches stay fast and work across restarts of the
underlying allocations.
