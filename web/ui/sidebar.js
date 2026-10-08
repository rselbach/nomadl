// sidebar.js shows the service and level facets. Checkbox state comes
// from the query, and a change asks the server to rewrite the query, so
// the search box stays the single source of truth.
import { useState } from 'preact/hooks';
import { html } from '../lib/html.js';
import { formatCount, LEVEL_LABELS } from '../lib/format.js';

const FILTER_THRESHOLD = 8;

// nextSelection picks the shortest clause for the checked values: none,
// an exclusion of the few unchecked ones, or an inclusion of the checked.
export function nextSelection(values, checked) {
  const unchecked = values.filter((v) => !checked.includes(v));
  if (unchecked.length === 0) {
    return { mode: 'all', values: [] };
  }
  if (checked.length > 0 && unchecked.length < checked.length) {
    return { mode: 'exclude', values: unchecked };
  }
  return { mode: 'include', values: checked };
}

export function Sidebar({ facets, onSelect, nomadReachable }) {
  if (!facets) {
    return html`<aside class="sidebar"><div class="sidebar-empty">Loading…</div></aside>`;
  }
  const byField = Object.fromEntries(facets.map((f) => [f.field, f]));
  return html`
    <aside class="sidebar" aria-label="Filters">
      ${byField.level && html`<${Facet} title="Level" facet=${byField.level} onSelect=${onSelect} />`}
      ${byField.service && html`<${Facet} title="Service" facet=${byField.service} onSelect=${onSelect} nomadReachable=${nomadReachable} />`}
    </aside>
  `;
}

function Facet({ title, facet, onSelect, nomadReachable }) {
  const [filter, setFilter] = useState('');
  const values = facet.values.map((v) => v.value);
  const checked = facet.values.filter((v) => v.selected).map((v) => v.value);
  const isLevel = facet.field === 'level';
  // Without Nomad there's no telling which services still run.
  const stopped = (v) => !isLevel && nomadReachable && !v.running;
  const shown = facet.values.filter((v) => v.value.toLowerCase().includes(filter.toLowerCase()));

  function toggle(value) {
    const next = checked.includes(value) ? checked.filter((v) => v !== value) : [...checked, value];
    onSelect(facet.field, nextSelection(values, next));
  }

  function only(value) {
    const alone = checked.length === 1 && checked[0] === value;
    onSelect(facet.field, alone ? { mode: 'all', values: [] } : { mode: 'include', values: [value] });
  }

  return html`
    <section class="facet">
      <header class="facet-header">
        <h2>${title}</h2>
        ${facet.mode !== 'all' && html`
          <button type="button" class="link-button" onClick=${() => onSelect(facet.field, { mode: 'all', values: [] })}>Reset</button>
        `}
      </header>
      ${facet.mode === 'custom' && html`
        <p class="facet-note">The query filters ${title.toLowerCase()} in a way these boxes can't show. Clicking one replaces it.</p>
      `}
      ${!isLevel && facet.values.length > FILTER_THRESHOLD && html`
        <input class="facet-filter" type="search" placeholder="Filter ${title.toLowerCase()}s" value=${filter}
          onInput=${(e) => setFilter(e.currentTarget.value)} aria-label="Filter ${title.toLowerCase()}s" />
      `}
      ${facet.values.length === 0 && html`<p class="facet-note">None yet.</p>`}
      <ul class="facet-values">
        ${shown.map((v) => html`
          <li class="facet-value ${v.selected ? '' : 'off'} ${stopped(v) ? 'stopped' : ''}">
            <input type="checkbox" checked=${v.selected} onChange=${() => toggle(v.value)} aria-label="Show ${v.value}" />
            ${isLevel && html`<span class="swatch lvl-${v.value}" aria-hidden="true"></span>`}
            <button type="button" class="facet-name" title="Show only ${isLevel ? LEVEL_LABELS[v.value] : v.value}" onClick=${() => only(v.value)}>
              ${isLevel ? LEVEL_LABELS[v.value] : v.value}
            </button>
            ${stopped(v) && html`<span class="badge" title="No task running in Nomad">stopped</span>`}
            <span class="facet-count">${formatCount(v.count)}</span>
          </li>
        `)}
      </ul>
    </section>
  `;
}
