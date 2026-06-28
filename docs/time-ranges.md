# Time Ranges And Live Mode

The time range input controls which stored logs are shown in the table and
timeline. It is separate from the query string.

## Relative Ranges

Relative ranges look backward from "now".

Examples:

```text
30s
1m
5m
15m
1h30m
1d
1w
1mo
```

Supported units:

| Unit | Meaning |
| --- | --- |
| `s` | seconds |
| `m` | minutes |
| `h` | hours |
| `d` | days |
| `w` | weeks |
| `mo` | 30-day months |

`1h30m` uses Go duration syntax and means 90 minutes.

## live

`live` means a 15 minute relative range and live mode. The window moves forward
as time passes.

```text
live
```

## all

`all` disables time filtering and shows all stored logs matching the other
filters.

```text
all
```

## Explicit Ranges

Explicit ranges pin both a start and end time. They are static.

Examples:

```text
2026-06-28 10:04 - 2026-06-28 10:09
2026-06-28 10:04:00 - 2026-06-28 10:09:00
2026-06-28T10:04:00Z - 2026-06-28T10:09:00Z
Jun 28, 10:04 am - Jun 28, 10:09 am
Jun 28, 2026, 10:04 am - Jun 28, 2026, 10:09 am
```

Supported separators are ` - `, an en dash, or an em dash surrounded by spaces.

## Top Bar Controls

The time control buttons operate on the current window.

| Control | Behavior |
| --- | --- |
| Back | Moves the range one full window into the past. |
| Pause/Play | Switches between live and static modes. |
| Forward | Moves a static range forward, capped at now. |
| Zoom out | Increases the visible range. |
| Refresh | Refreshes the table and timeline immediately. |

## Live Mode

In live mode:

- Background ingestion continues.
- The table and timeline auto-refresh every few seconds.
- Relative ranges keep moving with the clock.
- The mode label shows `Live`.

Click Pause to freeze the current window. The time input changes from a relative
range to an explicit start/end range.

## Static Mode

In static mode:

- Background ingestion continues.
- The table and timeline do not update automatically.
- Click Refresh to reload the current filters.
- Click Play to return to a relative live range with the same duration.
- The mode label shows `Static`.

Static mode is useful when reading an incident window and you do not want new
logs shifting the screen.

## Timeline Selection

Click or drag on the timeline to select a time range. This sets the time range
input to an explicit range and switches to static mode.

The selected range is applied to both the log table and timeline.
