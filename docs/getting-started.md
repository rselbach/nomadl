# Getting Started

This guide gets `nomadl` running against a Nomad cluster and walks through the
first search.

## Requirements

- A reachable HashiCorp Nomad HTTP API.
- A Nomad token if your cluster requires ACLs.
- For source builds, the Go version declared in `go.mod`.

## Install

### Homebrew

After a release is published, macOS users can install from the tap:

```sh
brew install rselbach/tap/nomadl
```

### Release Assets

Tagged releases publish these assets:

- macOS: `nomadl-<version>-darwin-amd64.zip`
- macOS Apple Silicon: `nomadl-<version>-darwin-arm64.zip`
- Debian/Ubuntu: `nomadl_<version>_amd64.deb` and `nomadl_<version>_arm64.deb`
- Fedora/RHEL: `nomadl-<version>.amd64.rpm` and `nomadl-<version>.arm64.rpm`
- Arch Linux: `nomadl-<version>-amd64.pkg.tar.zst` and `nomadl-<version>-arm64.pkg.tar.zst`
- Linux universal: `nomadl-<version>-x86_64.AppImage`

### Source

```sh
go build .
./nomadl --version
```

## Run Locally

```sh
nomadl
```

If running from source:

```sh
go run .
```

`nomadl` prints the server address, Nomad API address, config directory, database
path, and ingestion settings at startup.

Open the printed URL in a browser:

```text
http://127.0.0.1:7788
```

## Connect To Nomad

The Nomad client reads the standard Nomad environment variables supported by
HashiCorp's Go client, including:

- `NOMAD_ADDR`
- `NOMAD_TOKEN`
- `NOMAD_NAMESPACE`
- `NOMAD_REGION`

Example:

```sh
NOMAD_ADDR=https://nomad.example.com:4646 NOMAD_TOKEN=... nomadl
```

You can also override only the Nomad address with a flag:

```sh
nomadl --nomad-addr=https://nomad.example.com:4646
```

## First Run

On startup, `nomadl`:

1. Resolves the config directory.
2. Creates or opens the SQLite database.
3. Clears stored logs by default.
4. Discovers running Nomad jobs.
5. Backfills recent task logs.
6. Starts background log streams.
7. Serves the browser UI.

The first few seconds may show little or no data while discovery and backfill are
warming up.

## First Search

Try these in the search box:

```text
error
status:error
service:api
service:api status:error
"request timeout"
@trace_id:abc123
```

The stream filter defaults to `stderr`. If you expect stdout logs, start with
`--ingest-stdout` and choose `stdout` or `All Streams` in the UI.

## Stop nomadl

Stop the process with `Ctrl-C` in the terminal where it is running.

By default, a restart clears the local database and backfills again. Use
`--reset-on-start=false` if you want to keep the database across restarts.
