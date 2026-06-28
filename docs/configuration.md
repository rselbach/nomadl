# Configuration

`nomadl` is configured through CLI flags, Nomad environment variables, and a
small settings file written by the UI.

## CLI Flags

Run `nomadl --help` to see the current flag list.

| Flag | Default | Description |
| --- | --- | --- |
| `--addr` | `127.0.0.1:7788` | Address for the local web server. |
| `--nomad-addr` | empty | Nomad API address. If empty, the Nomad client uses its environment/default config. |
| `--db` | `<config-dir>/nomadl.db` | SQLite database path. |
| `--ingest` | `true` | Continuously ingest Nomad logs into SQLite. |
| `--reset-on-start` | `true` | Clear stored logs before starting. |
| `--backfill-bytes` | `262144` | Bytes to backfill per task stream on startup/discovery. |
| `--backfill-workers` | `2` | Maximum concurrent log backfill workers. |
| `--discover-interval` | `15s` | How often to discover new allocations. |
| `--ingest-services` | empty | Comma-separated services to ingest. Empty means all running services. |
| `--ingest-stdout` | `false` | Also ingest stdout. stderr is always ingested. |
| `--max-streams` | `16` | Maximum task log streams to ingest concurrently. `0` means unlimited. |
| `--priority-services` | `iam,idp,idp-hydra` | Comma-separated services to ingest first. |
| `--stream-start-delay` | `250ms` | Delay between starting live log streams. |
| `--version` | `false` | Print version and exit. |

## Nomad Environment Variables

`nomadl` uses HashiCorp's Nomad Go client. Standard Nomad client environment
variables are respected, including:

- `NOMAD_ADDR`
- `NOMAD_TOKEN`
- `NOMAD_NAMESPACE`
- `NOMAD_REGION`

Example:

```sh
NOMAD_ADDR=https://nomad.example.com:4646 NOMAD_TOKEN=... nomadl
```

The `--nomad-addr` flag overrides only the Nomad address. Tokens, namespace, and
region still come from the Nomad client environment/config behavior.

## Config Directory

The config directory is resolved as follows:

1. If `XDG_CONFIG_DIR` is set, use `$XDG_CONFIG_DIR/nomadl`.
2. Otherwise use `$HOME/.config/nomadl`.

The directory contains:

- `nomadl.db`: default SQLite database.
- `settings.json`: persisted UI ingestion settings.

Example with an isolated config directory:

```sh
XDG_CONFIG_DIR=/tmp/nomadl-config nomadl
```

## Settings File

The settings file currently stores the service allowlist chosen in the Settings
modal:

```json
{
  "ingest_services": ["api", "billing", "worker"]
}
```

An empty list means "ingest all running services".

## Database Path

Use `--db` to put the SQLite database somewhere else:

```sh
nomadl --db=/tmp/nomadl.db
```

This is useful for tests, demos, or keeping separate caches for separate Nomad
clusters.

## Ingestion Scope

By default, all running services are candidates for ingestion. You can restrict
this from the command line:

```sh
nomadl --ingest-services=api,billing,worker
```

Or from the Settings modal in the UI.

If both are used, the explicit `--ingest-services` flag wins at startup. Saving
settings from the UI updates the persisted allowlist and restarts ingestion with
the new selection.

## stdout And stderr

stderr is always ingested. stdout is opt-in:

```sh
nomadl --ingest-stdout
```

The UI stream filter can show `stderr`, `stdout`, or all streams, but stdout rows
only exist if stdout ingestion was enabled before those rows were emitted or
backfilled.

## Reset Behavior

The default `--reset-on-start=true` clears the local database every time `nomadl`
starts. This matches the current local-dev cache model: startup backfills recent
logs and then live ingestion keeps the store fresh.

To keep the local database across restarts:

```sh
nomadl --reset-on-start=false
```
