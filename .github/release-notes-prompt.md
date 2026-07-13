# Release Notes Prompt

Use this prompt with a prepared change context from git commits, PR metadata,
or a concise maintainer summary. The model should write only the release notes
Markdown.

```text
You are writing release notes for nomadl, a local browser UI for ingesting,
searching, and exploring HashiCorp Nomad allocation logs.

Write user-facing Markdown release notes for the new version.

Audience:
- Developers and operators using nomadl as a local/dev Nomad log viewer.
- They care about what changed, what got easier, what was fixed, and whether
  anything requires action.

Style:
- Clear, concise, pragmatic.
- Write for users, not just maintainers.
- Do not dump raw commit messages.
- Group related changes.
- Prefer concrete behavior over implementation details.
- Mention implementation details only when they explain user-visible behavior
  or upgrade impact.
- Do not invent changes that are not supported by the input.
- Do not mention internal tests unless they are directly relevant to users.
- Use Markdown.
- No emojis.

Format:

## <VERSION>

Start with a short 1-2 sentence summary of the release.

### Highlights

Include 2-5 bullets for the most important user-visible changes.

### Added

New user-visible capabilities.

### Changed

Behavior, UX, performance, packaging, or workflow improvements.

### Fixed

Bugs fixed in this release.

### Upgrade Notes

Include only if users need to know something before or after upgrading. If there
are no upgrade notes, write:
No configuration changes are required.

End with:
**Full changelog:** `<PREVIOUS_VERSION>...<VERSION>`

Rules:
- Return only the release notes Markdown.
- Do not wrap the release notes in a code fence.
- Omit empty sections except `Upgrade Notes`.
- Keep bullets short but specific.
- If a change fits multiple sections, put it in the section where it is most
  useful to a user.
- Preserve exact command examples when useful.
- If the input includes breaking changes, call them out clearly in the summary
  and `Upgrade Notes`.

Input:

Project:
nomadl

Previous version:
<PREVIOUS_VERSION>

New version:
<VERSION>

Changes:
<CHANGE_CONTEXT>
```

## Example Change Context

```text
Commits:
- Cache rendered log search results
- Persist search history and preferences
- Fix ANSI reset artifact in highlighted logs
- Add log view help popup
- Improve service list layout
- Match exact dotted JSON field names
- Open logs from initial target argument

Additional context:
- Users can run `nomadl <service-or-job-name>` to open logs directly.
- Search now correctly matches JSON field names containing dots, for example
  `@grpc.status_code:OK`.
- The log view help text moved from a long footer line to a popup opened with
  `?` or `h`.
- Search history and preferences are stored locally in `nomadl.db`.
- Preferences include wrap, follow, JSON, and log stream choices.
- Highlighted log lines no longer show a stray `[0m` ANSI reset artifact.
- The service/job list is more compact, with inline aligned stats.
- Search rendering was optimized to avoid repeatedly reparsing the same query
  and rerendering unchanged buffered logs.
```
