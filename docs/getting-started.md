# Getting Started

## Install

macOS via Homebrew:

```sh
brew install rselbach/tap/nomadl
```

Linux packages (`.deb`, `.rpm`, Arch) and an AppImage are attached to each
[GitHub release](https://github.com/rselbach/nomadl/releases), alongside
macOS zips for manual installs.

From source, using the Go version declared in `go.mod`:

```sh
go build .
```

## Run

```sh
nomadl
```

On startup `nomadl`:

1. Binds to `127.0.0.1:7788` (walking to the next free port if taken).
2. Discovers running Nomad services and starts ingesting their logs into a
   local SQLite database.
3. Opens the UI in your default browser (disable with `--open=false`).

## Connecting to Nomad

`nomadl` uses the standard Nomad client environment. With no configuration
it talks to a local agent at `http://127.0.0.1:4646`.

For a remote cluster:

```sh
NOMAD_ADDR=https://nomad.example.com:4646 NOMAD_TOKEN=... nomadl
```

or:

```sh
nomadl --nomad-addr=https://nomad.example.com:4646
```

Other standard Nomad variables (`NOMAD_TOKEN`, `NOMAD_REGION`,
`NOMAD_NAMESPACE`, TLS settings) are honored as well.

## First search

Type into the search box and press Enter. Some examples:

```text
error
level:error service:api
"connection refused" -health
```

See [Query syntax](query-syntax.md) for the full language and
[Using the UI](using-the-ui.md) for the histogram, live tail, and keyboard
shortcuts.
