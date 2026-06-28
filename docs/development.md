# Development And Releases

This guide is for contributors and maintainers.

## Local Development

Install dependencies through Go modules:

```sh
go mod download
```

Run the app:

```sh
just run
```

Or without `just`:

```sh
go run .
```

Run tests:

```sh
just test
```

Or:

```sh
go test ./...
```

Build:

```sh
just build
```

## Useful Local Runs

Use a temporary isolated config directory:

```sh
XDG_CONFIG_DIR=/tmp/nomadl-config go run .
```

Use an isolated database:

```sh
go run . --db=/tmp/nomadl.db
```

Restrict ingestion while developing:

```sh
go run . --ingest-services=api,billing --max-streams=4
```

Include stdout:

```sh
go run . --ingest-stdout
```

## Verification

Before handing off changes, run relevant checks:

```sh
just test
go vet ./...
golangci-lint run ./...
just build
```

For UI JavaScript syntax, this repository has used a small Node smoke that
extracts inline scripts from `web/index.html` and compiles them with
`new Function(...)`.

## Embedded Web Assets

The web UI is embedded by `web/embed.go`:

```go
//go:embed index.html
var IndexHTML []byte

//go:embed htmx.min.js
var HTMXJS []byte
```

Release binaries include the UI and local HTMX file. There is no runtime web
asset directory to copy.

## Release Workflow

The GitHub Actions release workflow lives at:

```text
.github/workflows/release.yml
```

It runs on tags matching:

```text
v*
```

The workflow:

1. Checks out the repository with full tag history.
2. Uses the Go version from `go.mod`.
3. Runs `go test ./...` and `go vet ./...`.
4. Builds Linux and macOS binaries.
5. Sets the binary version with `-X main.version=<version>`.
6. Packages Linux deb/rpm/Arch assets with `nfpm`.
7. Builds an AppImage.
8. Zips macOS binaries.
9. Generates checksums.
10. Generates release notes from git context.
11. Creates a GitHub release.
12. Updates the Homebrew formula in `rselbach/homebrew-tap`.

## Release Secrets

The workflow expects these secrets:

- `DEEPSEEK_API_KEY`: used by the release notes generation step.
- `HOMEBREW_TAP_TOKEN`: used to push formula updates to the Homebrew tap.

## Release Notes Prompt

The release notes prompt lives at:

```text
.github/release-notes-prompt.md
```

It is combined with git log, changed files, and diffstat for the tag range.

## Local Release Smoke

To check the release ldflags locally:

```sh
go build -ldflags "-s -w -X main.version=9.8.7" -o /tmp/nomadl .
/tmp/nomadl --version
```

Expected output:

```text
nomadl 9.8.7
```

Cross-compile smoke:

```sh
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  goos="${target%/*}"
  goarch="${target#*/}"
  GOOS="${goos}" GOARCH="${goarch}" \
    go build -ldflags "-s -w -X main.version=9.8.7" \
    -o "/tmp/nomadl-${goos}-${goarch}" .
done
```

Workflow lint:

```sh
actionlint .github/workflows/release.yml
```
