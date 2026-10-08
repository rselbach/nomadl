# nomadl

`nomadl` is a local, single-binary Nomad log explorer. It continuously ingests
HashiCorp Nomad allocation logs into a local SQLite database and serves a
browser UI for searching, filtering, inspecting, and graphing logs.

## Features

- Continuous ingestion: each running task's log is followed from a slice
  of recent history onward, and dropped streams reconnect without gaps.
- Local SQLite cache using `modernc.org/sqlite`, with row-count capping and
  automatic pruning.
- Query syntax with terms, phrases, boolean operators, negation, field
  filters, wildcards, numeric comparisons, ranges, and JSON attribute
  lookups.
- Level and service filters with counts that edit the search text, so the
  query always shows what is searched.
- Histogram stacked by level, with drag-to-zoom time selection.
- Live updates that never shift the lines you're reading.
- Details panel with JSON fields, one-click filters, trace filtering, and
  the surrounding lines from the same task.
- Searches kept in the URL, keyboard navigation, light and dark themes,
  and searching stored logs while Nomad is unreachable.

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
