# Using The UI

The UI is a local browser app served by `nomadl`. It reads from the local SQLite
store populated by background ingestion.

## Layout

The screen has these main areas:

- Top bar: query, time range, stream filter, live/static controls, diagnostics,
  theme toggle, table options, and settings.
- Left sidebar: service and status facets.
- Timeline: stacked bar chart of matching logs over time.
- Log table: newest matching log rows.
- Details drawer: metadata, parsed JSON fields, raw payload, and field actions.

## Query Bar

The query bar is the source of truth for search filtering. Service and status
facets rewrite `service:` and `status:` clauses in the query.

Examples:

```text
status:error
service:api status:error
"request timeout"
@trace_id:abc123
```

While typing, the UI suggests fields and values. Suggestions include known
services, status categories, streams, tasks, levels, allocations, and JSON
attributes seen in recent JSON logs.

## Stream Filter

The stream filter defaults to `stderr`.

Options:

- `stderr`: show stderr logs.
- `stdout`: show stdout logs.
- `All Streams`: show both streams.

stdout logs are only present when `nomadl` is started with `--ingest-stdout`.

## Live And Static Modes

The top bar has DataDog-style time controls:

- Back: move the current time window one window-width into the past.
- Pause/Play: switch between live mode and static mode.
- Forward: move the current static time window forward.
- Zoom out: increase the time window.
- Refresh: manually refresh logs and timeline.

Live mode keeps refreshing the table and timeline. Static mode keeps background
ingestion running but does not update the screen until you click Refresh or
switch back to live mode.

## Time Range Input

The time range input accepts relative ranges, `live`, `all`, and explicit ranges.

Examples:

```text
30s
5m
1h30m
1d
1w
1mo
all
2026-06-28 10:04 - 2026-06-28 10:09
Jun 28, 10:04 am - Jun 28, 10:09 am
```

See [Time ranges and live mode](time-ranges.md) for details.

## Service Facet

The Service facet lists visible running services. Checking or unchecking services
rewrites the query with a `service:` clause.

Useful interactions:

- Click a service name to isolate that service.
- Use Select/Clear to change visible services after filtering the facet list.
- Use the Settings modal to change which services are ingested in the background.

The service facet is a search filter, not the ingestion allowlist. Ingestion is
controlled by startup flags and Settings.

## Status Facet

The Status facet filters by status categories derived from the stored log level:

| Status | Matching levels |
| --- | --- |
| `emergency` | `EMERGENCY`, `ALERT`, `CRITICAL`, `CRIT`, `FATAL`, `PANIC` |
| `error` | `ERROR`, `ERR` |
| `warn` | `WARN`, `WARNING` |
| `notice` | `NOTICE` |
| `info` | `INFO` |
| `debug` | `DEBUG`, `TRACE` |
| `ok` | `OK`, `SUCCESS`, `UNKNOWN` |

Click a status label to isolate that status. Checking or unchecking statuses
rewrites the query with a `status:` clause.

## Timeline

The timeline shows matching log volume over the current time range. Bars are
stacked by status category.

Use the timeline to:

- Hover bars for bucket totals and per-status counts.
- Click or drag over bars to select an explicit time range.
- Switch from live to static mode when selecting a historical range.

The timeline uses the same query, stream, and time filters as the log table.

## Log Table

The table shows the newest matching rows, capped at 500. Columns are resizable
and can be hidden from Table Options. Column widths and visibility are stored in
browser `localStorage`.

Click a row to open the details drawer.

## Details Drawer

The details drawer shows:

- Metadata: time, service, task, level, stream, and row id.
- JSON fields if the raw payload is valid JSON.
- Message fallback for non-JSON rows.
- Raw payload.

Each field has an action menu. Actions include:

- Copy value.
- Copy `key:value`.
- Filter by this value.
- Exclude this value.
- Replace the whole query with this field filter.

JSON fields use `@field:value` query syntax.

## Settings Modal

Open Settings to choose which services are ingested.

Modes:

- Ingest all running services.
- Ingest only selected services.

Saving settings persists `settings.json`, restarts ingestion, refreshes the
service list, and refreshes the UI.

## Theme Toggle

Use the Light/Dark button to switch themes. The choice is stored in browser
`localStorage`.

## Diagnostics

Diagnostics checks the Nomad API connection and returns basic visibility
information. Use it when services or logs are missing.

## Clear

Clear removes all stored logs from the local SQLite database. Background
ingestion continues and will repopulate matching rows.
