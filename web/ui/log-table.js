// log-table.js lists log rows, newest first, one line each. Only the rows
// in view are rendered, so long sessions stay fast.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks';
import { html } from '../lib/html.js';
import { formatTime } from '../lib/format.js';

export const ROW_HEIGHT = 24;
const OVERSCAN = 12;
const NEAR_END_ROWS = 40;
const MIN_MESSAGE_WIDTH = 280;
// Matches the --col-* widths in app.css, in the order columns give way.
const COLUMN_WIDTHS = [['task', 108], ['service', 156], ['level', 64]];
const FIXED_WIDTH = 3 + 172;

// fitColumns hides optional columns, least useful first, until the
// message column keeps a readable width.
function fitColumns(wanted, width) {
  const cols = { ...wanted };
  let used = FIXED_WIDTH + COLUMN_WIDTHS.reduce((sum, [name, w]) => sum + (cols[name] ? w : 0), 0);
  for (const [name, w] of COLUMN_WIDTHS) {
    if (width - used >= MIN_MESSAGE_WIDTH) {
      break;
    }
    if (cols[name]) {
      cols[name] = false;
      used -= w;
    }
  }
  return cols;
}

export function LogTable({ rows, highlight, prefs, selectedId, onSelect, onOpen, onNearEnd, onScrollTop, pending, onShowPending, revealId, resetKey, empty }) {
  const scrollRef = useRef(null);
  const [view, setView] = useState({ top: 0, height: 600, width: 1200 });
  const matcher = useMemo(() => highlightMatcher(highlight), [highlight]);

  useLayoutEffect(() => {
    const el = scrollRef.current;
    const observer = new ResizeObserver(() => setView((v) => ({ ...v, height: el.clientHeight, width: el.clientWidth })));
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  // Keep the selected row in view when it moves by keyboard.
  useEffect(() => {
    if (revealId === null || revealId === undefined) {
      return;
    }
    const index = rows.findIndex((r) => r.id === revealId);
    const el = scrollRef.current;
    if (index < 0 || !el) {
      return;
    }
    const rowTop = index * ROW_HEIGHT;
    if (rowTop < el.scrollTop) {
      el.scrollTop = rowTop;
    } else if (rowTop + ROW_HEIGHT > el.scrollTop + el.clientHeight) {
      el.scrollTop = rowTop + ROW_HEIGHT - el.clientHeight;
    }
  }, [revealId]);

  // A new search starts at the newest row.
  useEffect(() => {
    scrollRef.current.scrollTop = 0;
    setView((v) => ({ ...v, top: 0 }));
  }, [resetKey]);

  function onScroll(event) {
    const el = event.currentTarget;
    setView((v) => ({ ...v, top: el.scrollTop, height: el.clientHeight }));
    onScrollTop(el.scrollTop < ROW_HEIGHT / 2);
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - NEAR_END_ROWS * ROW_HEIGHT) {
      onNearEnd();
    }
  }

  const first = Math.max(0, Math.floor(view.top / ROW_HEIGHT) - OVERSCAN);
  const last = Math.min(rows.length, Math.ceil((view.top + view.height) / ROW_HEIGHT) + OVERSCAN);
  const cols = fitColumns(prefs.columns, view.width);
  const template = [
    '3px',
    'var(--col-time)',
    cols.level && 'var(--col-level)',
    cols.service && 'var(--col-service)',
    cols.task && 'var(--col-task)',
    'minmax(0, 1fr)',
  ].filter(Boolean).join(' ');

  return html`
    <div class="table" style=${{ '--grid': template }}>
      <div class="table-head" role="row">
        <span></span>
        <span role="columnheader">Time${prefs.utc ? ' (UTC)' : ''}</span>
        ${cols.level && html`<span role="columnheader">Level</span>`}
        ${cols.service && html`<span role="columnheader">Service</span>`}
        ${cols.task && html`<span role="columnheader">Task</span>`}
        <span role="columnheader">Message</span>
      </div>
      ${pending > 0 && html`
        <button type="button" class="pending-pill" onClick=${() => { scrollRef.current.scrollTop = 0; onShowPending(); }}>
          ↑ ${pending.toLocaleString()} new ${pending === 1 ? 'line' : 'lines'}
        </button>
      `}
      <div class="table-body" ref=${scrollRef} onScroll=${onScroll} role="grid" aria-rowcount=${rows.length}>
        ${rows.length === 0 ? empty : html`
          <div style=${{ height: `${rows.length * ROW_HEIGHT}px`, position: 'relative' }}>
            <div style=${{ transform: `translateY(${first * ROW_HEIGHT}px)` }}>
              ${rows.slice(first, last).map((row) => html`
                <${Row} key=${row.id} row=${row} cols=${cols} utc=${prefs.utc} matcher=${matcher}
                  selected=${row.id === selectedId} onSelect=${onSelect} onOpen=${onOpen} />
              `)}
            </div>
          </div>
        `}
      </div>
    </div>
  `;
}

function Row({ row, cols, utc, matcher, selected, onSelect, onOpen }) {
  return html`
    <div
      class="row lvl-row-${row.level_bucket} ${selected ? 'selected' : ''}"
      role="row"
      aria-selected=${selected}
      onClick=${() => { onSelect(row); onOpen(row); }}
    >
      <span class="gutter lvl-${row.level_bucket}" aria-hidden="true"></span>
      <span class="cell time ${row.ts_inferred ? 'inferred' : ''}" title=${row.ts_inferred ? 'Estimated: the line has no timestamp of its own' : ''}>
        ${row.ts_inferred ? '~' : ''}${formatTime(row.ts, utc)}
      </span>
      ${cols.level && html`<span class="cell level lvl-text-${row.level_bucket}">${row.level === 'UNKNOWN' ? '' : row.level}</span>`}
      ${cols.service && html`<span class="cell service">${row.service}</span>`}
      ${cols.task && html`<span class="cell task">${row.task}</span>`}
      <span class="cell message">${highlightText(row.message, matcher)}</span>
    </div>
  `;
}

function highlightMatcher(terms) {
  const usable = (terms || []).filter((t) => t.length > 0);
  if (usable.length === 0) {
    return null;
  }
  const escaped = usable
    .sort((a, b) => b.length - a.length)
    .map((t) => t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
  return new RegExp(`(${escaped.join('|')})`, 'gi');
}

function highlightText(text, matcher) {
  if (!matcher) {
    return text;
  }
  const parts = text.split(matcher);
  return parts.map((part, i) => (i % 2 === 1 ? html`<mark>${part}</mark>` : part));
}
