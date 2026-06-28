# nomadl Documentation

`nomadl` is a local browser UI for Nomad logs. It discovers running Nomad jobs,
backfills recent task logs, keeps ingesting logs in the background, stores them
in SQLite, and lets you search them through a DataDog-like interface.

## Start Here

- [Getting started](getting-started.md): install, run, connect to Nomad, and do
  your first search.
- [Configuration](configuration.md): CLI flags, Nomad environment variables,
  local state paths, and ingestion settings.
- [Using the UI](using-the-ui.md): search bar, facets, timeline, live/static
  controls, log details, table options, settings, and themes.
- [Query syntax](query-syntax.md): complete syntax reference with examples.
- [Time ranges and live mode](time-ranges.md): relative ranges, explicit ranges,
  live/static behavior, navigation, and timeline selection.
- [Operations and storage](operations.md): ingestion model, SQLite behavior,
  release artifacts, and security considerations.
- [Troubleshooting](troubleshooting.md): common failures and how to diagnose
  them.
- [Development and releases](development.md): local development, tests, release
  workflow, and package outputs.

## Mental Model

`nomadl` has two separate jobs:

1. Ingest logs from Nomad into a local SQLite database.
2. Serve a local web UI that queries that database.

The UI does not fetch logs directly for every search. Background ingestion keeps
the database fresh. Searches, facets, the timeline, and details all read from the
local SQLite store.

## Default Behavior

- The web UI listens on `127.0.0.1:7788`.
- The database is stored in the nomadl config directory as `nomadl.db`.
- The database is cleared on startup by default.
- Recent stderr logs are backfilled on startup.
- stderr is always ingested.
- stdout is ingested only when `--ingest-stdout` is set.
- Running services are discovered every 15 seconds.
- The search result table shows the newest matching rows, capped at 500.
