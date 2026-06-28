# Query Syntax

`nomadl` supports a DataDog-style query language for filtering locally stored
logs.

## Quick Examples

```text
error
"request timeout"
service:api
service:(api OR worker)
status:error
status:(error OR warn)
-status:debug
service:api status:error "timeout"
(service:api OR service:worker) status:error
@trace_id:abc123
@http.status_code:[500 TO 599]
@duration_ms:>100
*:timeout
```

## Plain Terms

A plain term searches the parsed message text as a case-insensitive contains
match:

```text
timeout
```

Multiple terms are combined with implicit `AND`:

```text
timeout database
```

This means "message contains timeout and message contains database".

## Quoted Phrases

Use quotes for phrases or values containing spaces or special characters:

```text
"request timeout"
service:"api gateway"
```

Inside quoted strings, `\` escapes the next character.

## Boolean Operators

Use uppercase `AND`, `OR`, and `NOT`:

```text
status:error OR status:warn
service:api AND status:error
NOT status:debug
```

Parentheses group expressions:

```text
(service:api OR service:worker) status:error
```

Lowercase `and`, `or`, and `not` are treated as normal search terms.

## Negation

Use `-` or `NOT` to exclude matches:

```text
-status:debug
NOT service:worker
service:api -"health check"
```

## Fields

Use `field:value` to search a specific field.

| Field | Meaning | Matching behavior |
| --- | --- | --- |
| `service` | Nomad job/service id | Exact, case-insensitive unless wildcarded |
| `job` | Alias for `service` | Exact, case-insensitive unless wildcarded |
| `status` | Derived status category | Maps categories to log levels |
| `level` | Raw parsed log level | Exact, case-insensitive unless wildcarded |
| `task` | Nomad task name | Exact, case-insensitive unless wildcarded |
| `stream` | `stderr` or `stdout` | Exact, case-insensitive unless wildcarded |
| `message` | Parsed message | Contains by default |
| `content` | Alias for `message` | Contains by default |
| `raw` | Raw log line/payload | Contains by default |
| `alloc` | Allocation id | Exact, case-insensitive unless wildcarded |
| `alloc_id` | Allocation id | Exact, case-insensitive unless wildcarded |
| `allocation` | Allocation id | Exact, case-insensitive unless wildcarded |
| `*` | Full-ish text search | Searches message, raw, service, allocation, task, level, and stream |

Examples:

```text
service:api
job:api
task:web
stream:stderr
level:ERROR
message:timeout
raw:trace_id
alloc_id:123abc
*:timeout
```

## Status Categories

`status:` is a convenience field built from raw log levels.

| Query | Matching raw levels |
| --- | --- |
| `status:emergency` | `EMERGENCY`, `ALERT`, `CRITICAL`, `CRIT`, `FATAL`, `PANIC` |
| `status:error` | `ERROR`, `ERR` |
| `status:warn` | `WARN`, `WARNING` |
| `status:notice` | `NOTICE` |
| `status:info` | `INFO` |
| `status:debug` | `DEBUG`, `TRACE` |
| `status:ok` | `OK`, `SUCCESS`, `UNKNOWN` |

Examples:

```text
status:error
status:(error OR warn)
-status:debug
```

## Grouped Field Values

Use `field:(a OR b)` to search multiple values for the same field:

```text
service:(api OR worker)
status:(error OR warn)
stream:(stderr OR stdout)
```

## Wildcards

Unquoted `*` and `?` act as wildcards:

- `*` matches zero or more characters.
- `?` matches exactly one character.

Examples:

```text
service:api-*
task:web-?
level:ERR*
```

Quoted wildcards are treated literally:

```text
message:"*"
```

## Existence Checks

Use `field:*` to require that a field exists or is non-empty:

```text
level:*
@trace_id:*
```

Use negation to find rows where a field is absent or empty:

```text
-@trace_id:*
```

## JSON Attributes

JSON attributes are read from the raw payload when the raw payload is valid JSON.
Use `@field:value`:

```text
@trace_id:abc123
@request_id:*
@http.status_code:500
```

Dotted names work in two ways:

- A direct JSON key named `http.status_code`.
- A nested JSON path like `{ "http": { "status_code": 500 } }`.

If both exist, the direct key is checked first.

## Numeric Comparisons

Numeric comparisons work on numeric fields and JSON attributes:

```text
@duration_ms:>100
@duration_ms:>=250
@duration_ms:<1000
@duration_ms:<=5000
@http.status_code:>=500
```

The comparison value must be numeric.

## Numeric Ranges

Use `[lower TO upper]` for inclusive numeric ranges:

```text
@http.status_code:[500 TO 599]
@duration_ms:[100 TO 1000]
```

Use `*` for an open bound:

```text
@duration_ms:[100 TO *]
@duration_ms:[* TO 250]
```

## Autocomplete

Autocomplete suggests fields and values while typing. It can suggest:

- Known services from Nomad.
- Status categories.
- Streams.
- Stored levels, tasks, and allocation ids.
- JSON attribute names and JSON values observed in recent valid JSON logs.

Use arrow keys to choose a suggestion. Press Enter or Tab to accept it.

## Common Patterns

Find production-looking errors for one service:

```text
service:api status:error
```

Find errors except noisy health checks:

```text
status:error -"health check"
```

Find slow requests:

```text
@duration_ms:>1000
```

Find HTTP 5xx responses:

```text
@http.status_code:[500 TO 599]
```

Find logs for one trace:

```text
@trace_id:abc123
```

Search all stored text-ish fields:

```text
*:payment-failed
```
