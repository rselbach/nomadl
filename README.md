# nomadl

`nomadl` is a terminal UI for browsing HashiCorp Nomad services and jobs, then
tailing allocation logs from selected tasks.

## Requirements

- Go 1.25.5 or newer
- A reachable Nomad HTTP API

## Usage

```sh
go run .
```

By default, `nomadl` connects to `http://127.0.0.1:4646`. Configure another
Nomad server with flags or the standard Nomad environment variables:

```sh
NOMAD_ADDR=https://nomad.example.com:4646 NOMAD_TOKEN=... go run .
```

Available flags:

```text
-addr string
    Nomad HTTP API address (default "http://127.0.0.1:4646")
-max-lines int
    maximum log lines kept in memory (default 20000)
-namespace string
    Nomad namespace
-refresh duration
    service list refresh interval (default 15s)
-region string
    Nomad region
-tail-bytes int
    bytes of recent logs to read before following; 0 means future-only (default 8192)
-store-path string
    path to SQLite state database (default user config dir/nomadl/nomadl.db)
-token string
    Nomad ACL token
-type string
    log stream to show: stdout, stderr, or both (default "stderr")
```

The app also reads `NOMAD_ADDR`, `NOMAD_TOKEN`, `NOMAD_NAMESPACE`, and
`NOMAD_REGION`.

Search history and UI preferences are stored in a SQLite database at
`nomadl/nomadl.db` under the user config directory unless `-store-path` is set.

## Development

Run the tests with:

```sh
go test ./...
```

## License

MIT. See [LICENSE](LICENSE).
