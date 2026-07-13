# Query Syntax

The search box accepts a small query language. Bare terms match the log
message; everything else composes with boolean operators and field filters.

## Terms and phrases

```text
error                    match logs whose message contains "error"
"connection refused"     match the exact phrase
timeout retry            both terms must match (implicit AND)
```

Unquoted terms support wildcards: `*` matches any run of characters and `?`
matches a single character. Inside quotes, `*` and `?` are literal.

```text
conn*                    connect, connection, ...
level:err?r
```

## Boolean operators

`AND`, `OR`, and `NOT` (uppercase) combine terms; `-` is shorthand for
`NOT`, and parentheses group:

```text
error OR fatal
error -health
NOT debug
(timeout OR refused) service:api
```

Adjacent terms are ANDed, and `AND` binds tighter than `OR`.

## Field filters

`field:value` restricts a term to one field. Reserved fields:

| Field | Matching |
| --- | --- |
| `service`, `job` | exact |
| `task` | exact |
| `alloc`, `alloc_id`, `allocation` | exact |
| `stream` | exact (`stderr` or `stdout`) |
| `level` | level bucket (see below) |
| `message`, `content` | contains |
| `raw` | contains (the unparsed log line) |

A field can take a parenthesized group, which applies the field to every
term inside:

```text
service:(api OR worker)
level:(error OR warn)
```

### Levels

`level:` values are grouped into buckets, so `level:error` matches both
`ERROR` and `ERR`, `level:warn` matches `WARN` and `WARNING`,
`level:emergency` covers `FATAL`, `PANIC`, `CRITICAL`, and friends, and
`level:debug` includes `TRACE`. `level:*` matches any log that has a level
at all, and catch-all values (`ok`, `success`, `unknown`) match every
bucketed level.

## JSON attributes

For JSON-formatted log lines, prefix a field with `@` (or `#`) to match an
extracted attribute. Dotted paths reach nested objects:

```text
@trace_id:8a2f...
@http.status:500
@user.name:"Troy Barnes"
```

## Comparisons and ranges

Field values support numeric comparisons and inclusive ranges:

```text
@http.status:>=500
@duration_ms:>1000
@http.status:[400 TO 499]
```
