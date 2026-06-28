# nomadl

`nomadl` is a local, single-binary Nomad log explorer. It continuously ingests
HashiCorp Nomad allocation logs into a local SQLite database and serves a
DataDog-like browser UI for searching, filtering, inspecting, and graphing logs.

## Features

- Background log ingestion with startup backfill and live streaming.
- Local SQLite cache/index using `modernc.org/sqlite`.
- DataDog-style query syntax, including fields, negation, boolean operators,
  wildcards, JSON attributes, numeric comparisons, and ranges.
- Service and status facets where the query field is the source of truth.
- Editable time ranges, live/static modes, range navigation, zoom out, and
  manual refresh.
- Stacked timeline chart with drag/click range selection.
- Log details drawer with JSON field extraction and per-field query actions.
- Persisted ingestion settings, table layout, sidebar state, and light/dark
  theme preference.

## Quick Start

Run from source:

```sh
go run .
```

Then open the printed URL, usually:

```text
http://127.0.0.1:7788
```

By default, `nomadl` uses the Nomad Go client's environment configuration and
connects to the local Nomad agent if no address is provided. To point at another
Nomad API:

```sh
NOMAD_ADDR=https://nomad.example.com:4646 NOMAD_TOKEN=... go run .
```

Or use the explicit flag:

```sh
go run . --nomad-addr=https://nomad.example.com:4646
```

## Installation

Release builds publish macOS zips, Linux packages, and a Linux AppImage. macOS
users can install through Homebrew once a release is published:

```sh
brew install rselbach/tap/nomadl
```

For source builds, use the Go version declared in `go.mod`:

```sh
go build .
./nomadl --version
```

## Documentation

- [Documentation index](docs/index.md)
- [Getting started](docs/getting-started.md)
- [Configuration](docs/configuration.md)
- [Using the UI](docs/using-the-ui.md)
- [Query syntax](docs/query-syntax.md)
- [Time ranges and live mode](docs/time-ranges.md)
- [Operations and storage](docs/operations.md)
- [Troubleshooting](docs/troubleshooting.md)
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
