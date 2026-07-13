# Using the UI

The UI is a single page: a query bar and time range on top, a services
sidebar on the left, and the histogram above the log table.

## Services sidebar

The sidebar lists running Nomad services. Selecting services scopes both
search results and the histogram. The sidebar can be resized by dragging
its edge or hidden entirely.

The gear icon opens settings, where you choose which services `nomadl`
ingests. The selection persists in `settings.json` across runs.

## Searching

Type a query (see [Query syntax](query-syntax.md)) and press Enter.
Suggestions for fields and values appear as you type. The result count
shows how many rows matched; **Load more** pages through large result
sets.

## Time range and histogram

The histogram shows matching log volume over time, colored by level. Drag
across it to zoom into a range, or use the time-range control to pick or
edit one. **Clear** returns to the default window. While a log's details
are open, auto-refresh pauses so results don't shift under you.

## Live tail

Live tail follows selected services in real time. The status line shows
what is being tailed, and **Stop** ends the stream. The tail view caps how
many rows it keeps in the page, so long sessions stay responsive.

## Log details drawer

Clicking a row (or navigating with the keyboard) opens the details drawer
with the parsed fields and raw line. From the drawer you can:

- **Filter by trace** — one click toggles a filter for the log's trace ID,
  showing every log from the same trace.
- **Show context** — view the surrounding logs for the same task (±30
  seconds), regardless of the current query.

## Keyboard and display

- `j` / `k` move the selection down and up the log table; `Escape` closes
  the drawer.
- The UTC toggle switches timestamps between local time and UTC.
- The table options menu controls which columns are shown.

## Ingest status

The status panel reports what the ingester is doing: which allocations
were discovered, backfill progress, and the state of each live stream.
