// drawer.js shows one log row in full: its message, metadata and JSON
// fields with filter actions, the raw line, and the lines around it.
import { useEffect, useRef, useState } from 'preact/hooks';
import { html } from '../lib/html.js';
import { getJSON, isAbort } from '../lib/api.js';
import { formatTime, shortID } from '../lib/format.js';

const CONTEXT_LINES = 50;

export function Drawer({ row, prefs, traceFields, traceActive, onClose, onFilter, onTrace }) {
  const [mode, setMode] = useState('details');
  useEffect(() => setMode('details'), [row.id]);

  const raw = row.raw || row.message;
  const parsed = parseJSONObject(raw);
  const trace = parsed ? findTrace(parsed, raw, traceFields) : null;

  return html`
    <aside class="drawer" aria-label="Log details">
      <header class="drawer-head">
        <div class="drawer-title">
          <strong>${row.service}</strong><span class="dim"> / ${row.task}</span>
          <div class="drawer-sub">
            <span class="lvl-text-${row.level_bucket}">${row.level === 'UNKNOWN' ? 'no level' : row.level}</span>
            ${' · '}${row.ts_inferred ? '~' : ''}${formatTime(row.ts, prefs.utc)}
            ${' · '}${row.stream}${' · '}<span title=${row.alloc_id}>${shortID(row.alloc_id)}</span>
          </div>
        </div>
        <div class="drawer-actions">
          <button type="button" class="button ${mode === 'context' ? 'active' : ''}"
            title="Show the lines around this one, in log order"
            onClick=${() => setMode(mode === 'context' ? 'details' : 'context')}>Context</button>
          <button type="button" class="icon-button" aria-label="Close details" title="Close (Esc)" onClick=${onClose}>×</button>
        </div>
      </header>
      <div class="drawer-body">
        ${mode === 'context'
          ? html`<${ContextLines} row=${row} prefs=${prefs} />`
          : html`
            ${trace && html`
              <button type="button" class="button trace-button ${traceActive ? 'active' : ''}" onClick=${() => onTrace(trace.field, trace.value)}>
                ${traceActive ? 'Restore the previous query' : html`Show this trace <code>${trace.value}</code>`}
              </button>
            `}
            <section class="detail">
              <h3>Message</h3>
              <pre class="detail-message">${row.message}</pre>
            </section>
            <section class="detail">
              <h3>Fields</h3>
              <dl class="fields">
                <${Field} name="service" value=${row.service} onFilter=${onFilter} filterField="service" />
                <${Field} name="task" value=${row.task} onFilter=${onFilter} filterField="task" />
                <${Field} name="level" value=${row.level} onFilter=${onFilter} filterField="level" filterValue=${row.level_bucket} />
                <${Field} name="stream" value=${row.stream} onFilter=${onFilter} filterField="stream" />
                <${Field} name="alloc" value=${row.alloc_id} onFilter=${onFilter} filterField="alloc" />
                ${parsed && flatten(parsed).map(([key, value]) => html`
                  <${Field} key=${key} name=${'@' + key} value=${value} onFilter=${onFilter}
                    filterField=${isScalar(value) ? '@' + key : null} />
                `)}
              </dl>
            </section>
            <section class="detail">
              <h3>Raw <${CopyButton} text=${raw} /></h3>
              <pre class="detail-raw">${raw}</pre>
            </section>
          `}
      </div>
    </aside>
  `;
}

function Field({ name, value, filterField, filterValue, onFilter }) {
  const text = display(value);
  const filterText = filterValue ?? text;
  return html`
    <div class="field">
      <dt>${name}</dt>
      <dd>
        <span class="field-value">${text}</span>
        <span class="field-actions">
          ${filterField && html`
            <button type="button" class="icon-button" title="Only rows where ${filterField} is ${filterText}" aria-label="Filter by ${name}"
              onClick=${() => onFilter(filterField, filterText, false)}>+</button>
            <button type="button" class="icon-button" title="Hide rows where ${filterField} is ${filterText}" aria-label="Exclude ${name}"
              onClick=${() => onFilter(filterField, filterText, true)}>−</button>
          `}
          <${CopyButton} text=${text} />
        </span>
      </dd>
    </div>
  `;
}

function CopyButton({ text }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    } catch (err) {
      console.warn('copy failed', err);
    }
  }
  return html`<button type="button" class="icon-button copy" title="Copy" aria-label="Copy" onClick=${copy}>${copied ? '✓' : '⧉'}</button>`;
}

function ContextLines({ row, prefs }) {
  const [state, setState] = useState({ loading: true });
  const anchorRef = useRef(null);

  useEffect(() => {
    const controller = new AbortController();
    setState({ loading: true });
    getJSON('/api/context', { id: row.id, n: CONTEXT_LINES }, controller.signal)
      .then((body) => setState({ rows: body.rows }))
      .catch((err) => !isAbort(err) && setState({ error: err.message }));
    return () => controller.abort();
  }, [row.id]);

  useEffect(() => anchorRef.current?.scrollIntoView({ block: 'center' }), [state.rows]);

  if (state.loading) {
    return html`<p class="dim">Loading…</p>`;
  }
  if (state.error) {
    return html`<p class="error-text">${state.error}</p>`;
  }
  return html`
    <p class="dim context-note">${row.task} ${row.stream} in ${shortID(row.alloc_id)}, in log order.</p>
    <ol class="context-lines">
      ${state.rows.map((line) => html`
        <li key=${line.id} ref=${line.id === row.id ? anchorRef : null} class="lvl-row-${line.level_bucket} ${line.id === row.id ? 'anchor' : ''}">
          <span class="gutter lvl-${line.level_bucket}"></span>
          <span class="time">${formatTime(line.ts, prefs.utc)}</span>
          <span class="message">${line.message}</span>
        </li>
      `)}
    </ol>
  `;
}

function parseJSONObject(text) {
  const trimmed = text.trim();
  if (!trimmed.startsWith('{')) {
    return null;
  }
  try {
    const value = JSON.parse(trimmed);
    return value && typeof value === 'object' && !Array.isArray(value) ? value : null;
  } catch {
    return null;
  }
}

// flatten lists an object's leaves as [dotted.path, value], nested
// objects first expanded, arrays kept whole.
function flatten(obj, prefix = '') {
  return Object.keys(obj).sort().flatMap((key) => {
    const value = obj[key];
    const path = prefix ? `${prefix}.${key}` : key;
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      return flatten(value, path);
    }
    return [[path, value]];
  });
}

function isScalar(value) {
  return value === null || ['string', 'number', 'boolean'].includes(typeof value);
}

function display(value) {
  if (value === null) {
    return 'null';
  }
  return typeof value === 'object' ? JSON.stringify(value) : String(value);
}

// findTrace returns the first configured trace field present in the
// payload, matching flat keys ("dd.trace_id") and nested paths alike.
function findTrace(obj, raw, fields) {
  for (const field of fields || []) {
    let value = obj[field];
    if (value === undefined) {
      value = field.split('.').reduce((node, key) => (node && typeof node === 'object' ? node[key] : undefined), obj);
    }
    if (value === undefined || value === null || typeof value === 'object' || value === '') {
      continue;
    }
    if (typeof value === 'number' && !Number.isSafeInteger(value)) {
      // A 64-bit id written as a JSON number loses digits in JSON.parse;
      // read the exact digits from the raw text instead.
      const key = field.split('.').pop().replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
      const match = raw.match(new RegExp(`"${key}"\\s*:\\s*(\\d+)`));
      if (match) {
        value = match[1];
      }
    }
    return { field: '@' + field, value: String(value) };
  }
  return null;
}
