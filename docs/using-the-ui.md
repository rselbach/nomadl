# Using the UI

The UI is a single page: a toolbar with the search box, a sidebar with the
level and service filters, a histogram of matching log volume, the log
lines, and a details panel for the line you select. The search, time
range, and live state are kept in the URL, so a link reproduces the view
and a reload keeps it.

## Searching

Type a query (see [Query syntax](query-syntax.md)) into the search box. The
search runs when you pause typing or press Enter, and field and value
suggestions appear as you type (Tab accepts one). Matching text is
highlighted in the results.

If the query doesn't parse, the problem is pointed out under the search box
and the results of the last valid query stay on screen.

## Level and service filters

The sidebar lists the level groups and the services, each with the number
of matching lines. Counts for a field ignore the query's own filter on that
field, so you can see what checking another box would add.

- Click a checkbox to include or exclude a value.
- Click a name to show only that value; click it again to show all.
- **Reset** clears the filter for that field.

The checkboxes edit the search text itself, so the query always shows
exactly what is being searched. If the query filters a field in a way the
boxes can't show (a wildcard, say), the sidebar says so, and clicking a box
replaces that filter.

Services come from stored logs and from Nomad. When Nomad is reachable,
services with no running task are marked *stopped*.

## Time range and histogram

The time menu picks a recent window or all stored logs. The histogram
shows matching lines over that window, stacked by level with the most
severe at the bottom; hover a bar for its counts. Drag across the
histogram, or click a bar, to zoom into that time; **Clear time selection**
returns to the previous window.

## Live updates

Live updates are on by default: new matching lines appear as they're
stored. While you're at the top of the list they appear at the top; when
you've scrolled down or have a line open, they wait behind a *new lines*
button so nothing moves under you. Live updates reconnect on their own
after a dropped connection and pick up where they left off.

**Live** (or the `l` key) turns them off, which freezes the view as a
snapshot; the URL then carries `live=0`. Zooming into a past time range
also turns them off.

## Log lines

Lines are newest first, one per row, with a colored edge for the level.
Times show milliseconds; a time marked `~` is an estimate because the line
had no timestamp (see [Operations and storage](operations.md)). Scrolling
to the end loads more lines.

Click a line, or select it with `j`/`k` and press Enter, to open the
details panel:

- **Message** and **Raw** show the full line.
- **Fields** lists the service, task, level, stream, and allocation, plus
  every attribute of a JSON line. Hover a field to add a filter for its
  value (`+`), exclude it (`−`), or copy it.
- **Show this trace** appears when the line carries a trace id (see the
  trace fields setting) and filters to every line of that trace; the same
  button restores the previous search.
- **Context** shows the lines around this one from the same task stream,
  in log order, without changing your search.

## Toolbar menu

The `⋯` menu opens settings, the ingest status, and keyboard help; picks
which columns to show and whether times are in UTC; and deletes all stored
logs.

The ingest indicator next to it summarizes ingestion: the number of
streams being followed, how many are waiting because of the stream cap,
or that Nomad is unreachable. Click it for details. When Nomad can't be
reached, a banner says so and the stored logs stay searchable.

## Settings

- **Services to ingest**: all running services, or a chosen list. Saving
  starts and stops streams right away.
- **Trace id fields**: the JSON attributes that hold a trace id, in order
  of preference.

## Keyboard

| Key | Action |
| --- | --- |
| `/` | Focus the search box |
| `Enter` | Run the search now, or open the selected line |
| `j` / `k` | Select the next or previous line |
| `Esc` | Close the details panel or a dialog |
| `l` | Turn live updates on or off |
| `?` | Show keyboard and search help |
