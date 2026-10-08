// query-bar.js is the search input: it commits the query after a pause
// in typing or on Enter, offers field and value suggestions from the
// server, and points at the problem when the query doesn't parse.
import { useEffect, useRef, useState } from 'preact/hooks';
import { html } from '../lib/html.js';
import { getJSON, isAbort } from '../lib/api.js';
import { byteOffset, charIndex } from '../lib/format.js';

const COMMIT_DELAY = 300;
const SUGGEST_DELAY = 120;

export function QueryBar({ draft, setDraft, onCommit, error, inputRef }) {
  const [suggest, setSuggest] = useState(null); // { items, active, start, end }
  const commitTimer = useRef(0);
  const suggestTimer = useRef(0);
  const suggestAbort = useRef(null);

  useEffect(() => () => {
    clearTimeout(commitTimer.current);
    clearTimeout(suggestTimer.current);
    suggestAbort.current?.abort();
  }, []);

  function queueCommit(value) {
    clearTimeout(commitTimer.current);
    commitTimer.current = setTimeout(() => onCommit(value), COMMIT_DELAY);
  }

  function commitNow(value) {
    clearTimeout(commitTimer.current);
    onCommit(value);
  }

  function closeSuggestions() {
    clearTimeout(suggestTimer.current);
    suggestAbort.current?.abort();
    setSuggest(null);
  }

  function queueSuggest() {
    clearTimeout(suggestTimer.current);
    suggestTimer.current = setTimeout(loadSuggestions, SUGGEST_DELAY);
  }

  async function loadSuggestions() {
    const input = inputRef.current;
    if (!input || document.activeElement !== input) {
      return;
    }
    suggestAbort.current?.abort();
    const controller = new AbortController();
    suggestAbort.current = controller;
    try {
      const body = await getJSON('/api/query-suggestions', {
        q: input.value,
        cursor: String(byteOffset(input.value, input.selectionStart ?? input.value.length)),
      }, controller.signal);
      const items = body.suggestions || [];
      setSuggest(items.length ? { items, active: 0, start: body.replace_start, end: body.replace_end } : null);
    } catch (err) {
      if (!isAbort(err)) {
        setSuggest(null);
      }
    }
  }

  function accept(item) {
    const input = inputRef.current;
    const value = input.value;
    const start = charIndex(value, suggest.start);
    const end = charIndex(value, suggest.end);
    const next = value.slice(0, start) + item.replacement + value.slice(end);
    const cursor = start + item.replacement.length;
    setDraft(next);
    setSuggest(null);
    queueCommit(next);
    requestAnimationFrame(() => {
      input.focus();
      input.setSelectionRange(cursor, cursor);
      queueSuggest();
    });
  }

  function onKeyDown(event) {
    if (suggest) {
      const count = suggest.items.length;
      switch (event.key) {
        case 'ArrowDown':
          event.preventDefault();
          setSuggest({ ...suggest, active: (suggest.active + 1) % count });
          return;
        case 'ArrowUp':
          event.preventDefault();
          setSuggest({ ...suggest, active: (suggest.active - 1 + count) % count });
          return;
        case 'Tab':
          event.preventDefault();
          accept(suggest.items[suggest.active]);
          return;
        case 'Escape':
          event.preventDefault();
          event.stopPropagation();
          closeSuggestions();
          return;
        default:
      }
    }
    if (event.key === 'Enter') {
      event.preventDefault();
      closeSuggestions();
      commitNow(event.currentTarget.value);
    } else if (event.key === 'Escape') {
      event.currentTarget.blur();
    }
  }

  return html`
    <div class="query ${error ? 'has-error' : ''}">
      <span class="query-prompt" aria-hidden="true">›</span>
      <input
        ref=${inputRef}
        class="query-input"
        type="text"
        value=${draft}
        placeholder='Search: timeout service:api level:error -debug "connection refused"'
        aria-label="Search logs"
        autocomplete="off"
        spellcheck="false"
        onInput=${(e) => { setDraft(e.currentTarget.value); queueCommit(e.currentTarget.value); queueSuggest(); }}
        onFocus=${queueSuggest}
        onClick=${queueSuggest}
        onBlur=${() => setTimeout(closeSuggestions, 150)}
        onKeyDown=${onKeyDown}
      />
      ${error && html`<${QueryError} query=${draft} error=${error} />`}
      ${suggest && html`
        <ul class="suggestions" role="listbox">
          ${suggest.items.map((item, i) => html`
            <li
              role="option"
              aria-selected=${i === suggest.active}
              class=${i === suggest.active ? 'active' : ''}
              onMouseDown=${(e) => { e.preventDefault(); accept(item); }}
              onMouseEnter=${() => setSuggest({ ...suggest, active: i })}
            >
              <span class="suggestion-label">${item.label}</span>
              <span class="suggestion-detail">${item.detail}</span>
            </li>
          `)}
        </ul>
      `}
    </div>
  `;
}

// QueryError quotes the query around the problem and underlines it.
function QueryError({ query, error }) {
  if (error.pos === undefined || error.pos === null) {
    return html`<div class="query-error" role="alert">${error.message}</div>`;
  }
  const at = charIndex(query, error.pos);
  const from = Math.max(0, at - 24);
  const to = Math.min(query.length, at + 24);
  return html`
    <div class="query-error" role="alert">
      <span>${error.message}</span>
      <code>
        ${from > 0 ? '…' : ''}${query.slice(from, at)}<mark>${query.slice(at, at + 1) || ' '}</mark>${query.slice(at + 1, to)}${to < query.length ? '…' : ''}
      </code>
      <span class="query-error-note">Showing results for the last valid query.</span>
    </div>
  `;
}
