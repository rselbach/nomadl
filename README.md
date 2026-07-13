# nomadl

`nomadl` is a local, single-binary Nomad log explorer. It continuously ingests
HashiCorp Nomad allocation logs into a local SQLite database and serves a
browser UI for searching, filtering, inspecting, and graphing logs.

## Features

- Background log ingestion with startup backfill and live streaming.
- Local SQLite cache using `modernc.org/sqlite`, with row-count capping and
  automatic pruning.
- Query syntax with terms, phrases, boolean operators, negation, field
  filters, wildcards, numeric comparisons, ranges, and JSON attribute
  lookups.
- Service sidebar, level filtering, and query suggestions.
- Time-range filtering with a histogram supporting drag-to-zoom selection.
- Live tail with pause-on-inspect and a capped DOM for long sessions.
- Log details drawer with one-click trace filtering and a ±30s context view.
- Keyboard navigation (`j`/`k`), UTC/local time toggle, and pagination with
  result counts.
- Ingest status panel showing per-stream progress.

## Quick Start

Run from source:

```sh
go run .
```

The UI opens in your default browser, usually at:

```text
http://127.0.0.1:7788
```

If the default port is taken, `nomadl` walks forward to the next free one.

By default, `nomadl` uses the Nomad Go client's environment configuration and
connects to the local Nomad agent if no address is provided. To point at
another Nomad API:

```sh
NOMAD_ADDR=https://nomad.example.com:4646 NOMAD_TOKEN=... go run .
```

Or use the explicit flag:

```sh
go run . --nomad-addr=https://nomad.example.com:4646
```

## Installation

Release builds publish macOS zips, Linux deb/rpm/Arch packages, and a Linux
AppImage. macOS users can install through Homebrew:

```sh
brew install rselbach/tap/nomadl
```

For source builds, use the Go version declared in `go.mod`:

```sh
go build .
```

## Documentation

- [Documentation index](docs/index.md)
- [Getting started](docs/getting-started.md)
- [Configuration](docs/configuration.md)
- [Query syntax](docs/query-syntax.md)
- [Using the UI](docs/using-the-ui.md)
- [Operations and storage](docs/operations.md)
- [Development and releases](docs/development.md)

## Development

```sh
just test
just build
```

If `just` is not available:

```sh
go test ./...
go build ./...
```

## License

MIT. See [LICENSE](LICENSE).
