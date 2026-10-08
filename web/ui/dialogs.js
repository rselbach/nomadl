// dialogs.js holds the settings, ingest status, and keyboard help dialogs.
import { useEffect, useRef, useState } from 'preact/hooks';
import { html } from '../lib/html.js';
import { getJSON, postJSON } from '../lib/api.js';
import { formatCount } from '../lib/format.js';

// Modal renders a native <dialog>, which brings focus trapping and Esc.
function Modal({ title, onClose, children, footer }) {
  const ref = useRef(null);
  useEffect(() => {
    const dialog = ref.current;
    dialog.showModal();
    return () => dialog.close();
  }, []);
  return html`
    <dialog ref=${ref} class="modal" onClose=${onClose} onClick=${(e) => e.target === ref.current && onClose()}>
      <header class="modal-head">
        <h2>${title}</h2>
        <button type="button" class="icon-button" aria-label="Close" onClick=${onClose}>×</button>
      </header>
      <div class="modal-body">${children}</div>
      ${footer && html`<footer class="modal-foot">${footer}</footer>`}
    </dialog>
  `;
}

export function SettingsDialog({ onClose, onSaved }) {
  const [settings, setSettings] = useState(null);
  const [all, setAll] = useState(true);
  const [chosen, setChosen] = useState([]);
  const [traceFields, setTraceFields] = useState('');
  const [filter, setFilter] = useState('');
  const [status, setStatus] = useState({ text: 'Loading…' });

  useEffect(() => {
    getJSON('/api/settings')
      .then((body) => {
        setSettings(body);
        setAll(body.ingest_services.length === 0);
        setChosen(body.ingest_services);
        setTraceFields(body.trace_fields.join(', '));
        setStatus(body.nomad_error ? { text: `Nomad is unreachable, so this list may be incomplete: ${body.nomad_error}`, error: true } : {});
      })
      .catch((err) => setStatus({ text: err.message, error: true }));
  }, []);

  async function save() {
    const services = all ? [] : chosen;
    if (!all && services.length === 0) {
      setStatus({ text: 'Choose at least one service, or ingest all of them.', error: true });
      return;
    }
    setStatus({ text: 'Saving…' });
    try {
      await postJSON('/api/settings', {
        ingest_services: services,
        trace_fields: traceFields.split(',').map((f) => f.trim()).filter(Boolean),
      });
      onSaved();
      onClose();
    } catch (err) {
      setStatus({ text: err.message, error: true });
    }
  }

  const available = settings?.available_services || [];
  const shown = available.filter((s) => s.toLowerCase().includes(filter.toLowerCase()));
  const setShown = (on) => setChosen(on
    ? [...new Set([...chosen, ...shown])]
    : chosen.filter((s) => !shown.includes(s)));

  return html`
    <${Modal} title="Settings" onClose=${onClose} footer=${html`
      <span class="modal-status ${status.error ? 'error-text' : ''}">${status.text}</span>
      <button type="button" class="button" onClick=${onClose}>Cancel</button>
      <button type="button" class="button primary" onClick=${save} disabled=${!settings}>Save</button>
    `}>
      <fieldset class="setting">
        <legend>Services to ingest</legend>
        <label class="check">
          <input type="checkbox" checked=${all} onChange=${(e) => setAll(e.currentTarget.checked)} />
          All running services
        </label>
        ${!all && html`
          <div class="service-picker">
            <div class="picker-tools">
              <input type="search" placeholder="Filter services" value=${filter} onInput=${(e) => setFilter(e.currentTarget.value)} aria-label="Filter services" />
              <button type="button" class="link-button" onClick=${() => setShown(true)}>Select shown</button>
              <button type="button" class="link-button" onClick=${() => setShown(false)}>Clear shown</button>
            </div>
            <ul class="picker-list">
              ${shown.map((service) => html`
                <li><label class="check">
                  <input type="checkbox" checked=${chosen.includes(service)}
                    onChange=${(e) => setChosen(e.currentTarget.checked ? [...chosen, service] : chosen.filter((s) => s !== service))} />
                  ${service}
                </label></li>
              `)}
              ${shown.length === 0 && html`<li class="dim">No services found.</li>`}
            </ul>
          </div>
        `}
      </fieldset>
      <fieldset class="setting">
        <legend>Trace id fields</legend>
        <input type="text" class="wide" value=${traceFields} onInput=${(e) => setTraceFields(e.currentTarget.value)}
          placeholder="dd.trace_id, trace_id" aria-describedby="trace-help" />
        <p id="trace-help" class="dim">JSON attributes that hold a trace id, in order of preference, separated by commas. The details panel offers to show every line of the trace it finds.</p>
      </fieldset>
    <//>
  `;
}

export function StatusDialog({ status, onClose }) {
  if (!status) {
    return html`<${Modal} title="Ingest status" onClose=${onClose}><p class="dim">Loading…</p><//>`;
  }
  const active = status.active_streams || [];
  const waiting = status.waiting_streams || [];
  return html`
    <${Modal} title="Ingest status" onClose=${onClose}>
      <dl class="status-grid">
        <dt>Nomad</dt>
        <dd>${status.nomad_addr}${status.nomad_error && html`<div class="error-text">${status.nomad_error}</div>`}</dd>
        <dt>Last discovery</dt><dd>${status.last_discovery || 'not yet'}</dd>
        <dt>Stored lines</dt><dd>${formatCount(status.db_rows)}</dd>
        <dt>Ingestion</dt>
        <dd>${status.ingest_enabled ? 'on' : 'off (-ingest=false)'}</dd>
        <dt>Services</dt>
        <dd>${status.ingest_services?.length ? status.ingest_services.join(', ') : 'all running services'}</dd>
        <dt>Streams</dt>
        <dd>${(status.streams || []).join(', ')} · ${active.length} following${status.max_streams > 0 ? ` of ${status.max_streams} max` : ''}</dd>
      </dl>
      ${waiting.length > 0 && html`
        <h3 class="warn-text">${waiting.length} waiting: over the stream cap (--max-streams)</h3>
        <ul class="stream-list">${waiting.map((s) => html`<li>${s}</li>`)}</ul>
      `}
      ${active.length > 0 && html`
        <h3>Following</h3>
        <ul class="stream-list">${active.map((s) => html`<li>${s}</li>`)}</ul>
      `}
    <//>
  `;
}

const SHORTCUTS = [
  ['/', 'Focus the search box'],
  ['Enter', 'Run the search now, or open the selected line'],
  ['j / k', 'Select the next or previous line'],
  ['Esc', 'Close the details panel or dialog'],
  ['l', 'Turn live updates on or off'],
  ['?', 'Show this help'],
];

const SYNTAX = [
  ['timeout retry', 'messages containing both words'],
  ['"connection refused"', 'an exact phrase'],
  ['conn*', 'wildcards match anywhere in the message'],
  ['error OR fatal', 'either term; AND binds tighter'],
  ['-health', 'exclude a term'],
  ['service:api level:error', 'fields; level matches groups like ERR and ERROR'],
  ['service:(api OR web)', 'several values of one field'],
  ['@http.status:>=500', 'JSON attributes, with comparisons'],
  ['@duration_ms:[100 TO 500]', 'numeric ranges'],
];

export function HelpDialog({ onClose }) {
  return html`
    <${Modal} title="Keyboard and search" onClose=${onClose}>
      <dl class="help-grid">
        ${SHORTCUTS.map(([key, text]) => html`<dt><kbd>${key}</kbd></dt><dd>${text}</dd>`)}
      </dl>
      <h3>Search syntax</h3>
      <dl class="help-grid">
        ${SYNTAX.map(([example, text]) => html`<dt><code>${example}</code></dt><dd>${text}</dd>`)}
      </dl>
    <//>
  `;
}
