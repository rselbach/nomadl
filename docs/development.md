# Development and Releases

## Building and testing

The `justfile` wraps the common tasks:

```sh
just build      # go build ./...
just test       # go test ./...
just run        # go run . [args]
```

Without `just`, the underlying Go commands work directly. Use the Go
version declared in `go.mod`.

## Layout

- `main.go` — flag parsing, listener setup (with port fallback), browser
  launch.
- `internal/appconfig` — config directory resolution and persisted
  settings.
- `internal/nomad` — Nomad API client, allocation discovery, log
  streaming, and line parsing.
- `internal/store` — SQLite store, search query parser and SQL builder,
  suggestions.
- `internal/server` — HTTP server, API handlers, and the ingest
  orchestrator.
- `web/` — the single-page UI (`index.html` plus a vendored `htmx`),
  embedded into the binary.

## Releases

Pushing a `v*` tag runs `.github/workflows/release.yml`, which:

1. Runs `go test ./...` and `go vet ./...`.
2. Cross-compiles for `linux/amd64`, `linux/arm64`, `darwin/amd64`, and
   `darwin/arm64`.
3. Packages Linux builds as `.deb`, `.rpm`, and Arch packages with `nfpm`,
   plus an `amd64` AppImage; macOS builds ship as zips. A
   `checksums.txt` covers all assets.
4. Generates release notes from the commit range with the prompt in
   `.github/release-notes-prompt.md` (requires the `DEEPSEEK_API_KEY`
   secret).
5. Creates the GitHub release and updates the Homebrew formula in
   `rselbach/homebrew-tap` (requires the `HOMEBREW_TAP_TOKEN` secret).

To cut a release:

```sh
git tag v1.1.0
git push origin v1.1.0
```
