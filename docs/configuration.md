# Configuration

`nomadl` is configured through command-line flags, standard Nomad
environment variables, and a small persisted settings file.

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--addr` | `127.0.0.1:7788` | Address to listen on. When left at the default and the port is busy, `nomadl` walks up to the next free port. An explicitly set address never moves. |
| `--nomad-addr` | (env) | Nomad API address. Defaults to the Nomad client environment (`NOMAD_ADDR`), falling back to the local agent. |
| `--db` | `<config dir>/nomadl.db` | Path to the SQLite database. |
| `--ingest` | `true` | Continuously ingest Nomad logs into SQLite. |
| `--reset-on-start` | `true` | Clear stored logs before starting. |
| `--backfill-bytes` | `262144` | Bytes to backfill per task stream on startup. |
| `--backfill-workers` | `2` | Maximum concurrent log backfills. |
| `--discover-interval` | `15s` | How often to discover new allocations. |
| `--ingest-services` | (all) | Comma-separated services to ingest. Overrides the persisted setting. |
| `--ingest-stdout` | `false` | Also ingest stdout; stderr is always ingested. |
| `--open` | `true` | Open the UI in the default browser on startup. |
| `--max-rows` | `200000` | Maximum stored log rows; oldest are pruned. `0` disables the cap. |
| `--max-streams` | `16` | Maximum task log streams ingested concurrently. `0` is unlimited, which can hit Nomad connection limits. |
| `--priority-services` | (none) | Comma-separated services to ingest first. |
| `--stream-start-delay` | `250ms` | Delay between starting live log streams. |

## Environment

The Nomad connection uses the official Go client's environment handling:
`NOMAD_ADDR`, `NOMAD_TOKEN`, `NOMAD_REGION`, `NOMAD_NAMESPACE`, and the TLS
variables all work as they do with the `nomad` CLI.

## Config directory and persisted settings

The config directory is `$XDG_CONFIG_HOME/nomadl` when `XDG_CONFIG_HOME` is
set, otherwise `~/.config/nomadl`. It holds:

- `nomadl.db` — the SQLite log store (unless `--db` points elsewhere).
- `settings.json` — settings saved from the UI. Currently the list of
  services to ingest (`ingest_services`), which the `--ingest-services`
  flag overrides for a single run.
