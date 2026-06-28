# Troubleshooting

Use this guide when services, logs, search, or release builds behave strangely.

## The UI Opens But No Services Appear

Check the Nomad API address printed at startup:

```text
nomad API: http://127.0.0.1:4646
```

If that is wrong, set `NOMAD_ADDR` or `--nomad-addr`:

```sh
NOMAD_ADDR=https://nomad.example.com:4646 nomadl
```

Then click Diagnostics in the UI. Diagnostics checks whether jobs and allocations
are visible from the current credentials.

Common causes:

- Wrong Nomad address.
- Missing or expired `NOMAD_TOKEN`.
- Wrong `NOMAD_NAMESPACE` or `NOMAD_REGION`.
- No running jobs visible to the token.

## Services Appear But No Logs Appear

First wait a few seconds. Startup discovery and backfill are asynchronous.

Then check:

- The stream filter defaults to `stderr`.
- stdout requires `--ingest-stdout`.
- The time range may exclude the logs.
- The query may include a restrictive `service:` or `status:` clause.
- The Settings allowlist may exclude the service.

Try these quick resets:

```text
q: empty
stream: All Streams
time_range: all
```

If stdout is needed, restart with:

```sh
nomadl --ingest-stdout
```

## Logs Are Delayed

Background ingestion is intentionally throttled.

Relevant flags:

- `--discover-interval`
- `--backfill-workers`
- `--max-streams`
- `--stream-start-delay`

If the cluster has many tasks and only 16 streams are allowed, some tasks may not
be streamed immediately. Increase `--max-streams` carefully.

## Nomad Rate Limits Or Connection Limits

Reduce concurrency and increase startup delay:

```sh
nomadl --max-streams=8 --backfill-workers=1 --stream-start-delay=500ms
```

Restrict ingestion to the services you care about:

```sh
nomadl --ingest-services=api,billing,worker
```

Or use the Settings modal.

## Search Syntax Errors

Common syntax problems:

- Missing closing quote.
- Missing closing parenthesis.
- Lowercase `or` when you meant uppercase `OR`.
- Invalid numeric comparison value.
- Invalid range syntax.

Examples of valid syntax:

```text
service:(api OR worker)
@duration_ms:>100
@http.status_code:[500 TO 599]
```

See [Query syntax](query-syntax.md).

## JSON Fields Do Not Appear In Details

The details drawer parses the raw payload as JSON. JSON fields appear only when
the raw log line is valid JSON.

If your application prefixes JSON with timestamps or other text, the line is not
valid JSON from `nomadl`'s perspective and will be shown as message/raw text.

## Timeline Is Empty

The timeline uses the same filters as the table. If it is empty, check:

- Query text.
- Service/status facets.
- Stream filter.
- Time range.
- Whether ingestion has warmed up.

Try `time_range=all` and an empty query.

## Static Mode Is Not Updating

That is expected. Static mode keeps ingestion running but does not refresh the
screen automatically. Click Refresh or switch to live mode.

## The Database Keeps Disappearing On Restart

This is the default behavior. `--reset-on-start=true` clears the database at
startup.

Use:

```sh
nomadl --reset-on-start=false
```

## Config Or Database Path Is Unexpected

`nomadl` uses:

1. `$XDG_CONFIG_DIR/nomadl` when `XDG_CONFIG_DIR` is set.
2. `$HOME/.config/nomadl` otherwise.

The startup log prints both the config directory and database path.

## Homebrew Formula Test Fails

The formula test expects:

```sh
nomadl --version
```

to print:

```text
nomadl <version>
```

If this fails in a release, check the release workflow ldflags:

```text
-X main.version=<version>
```

## AppImage Does Not Start

The AppImage runs the embedded `nomadl` binary. Start it from a terminal to see
errors:

```sh
./nomadl-<version>-x86_64.AppImage
```

Common causes are missing Nomad environment variables or inability to reach the
Nomad API from that shell.
