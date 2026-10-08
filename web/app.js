// app.js wires the page together: it owns the query, time range, and live
// state (mirrored in the URL), fetches results, and routes actions from
// the components.
import { render } from 'preact';
import { useCallback, useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { html } from './lib/html.js';
import { APIError, getJSON, isAbort, postJSON } from './lib/api.js';
import { RANGES, loadPrefs, rangeParams, readURL, savePrefs, writeURL } from './lib/state.js';
import { formatCount } from './lib/format.js';
import { QueryBar } from './ui/query-bar.js';
import { Sidebar } from './ui/sidebar.js';
import { Histogram } from './ui/histogram.js';
import { LogTable } from './ui/log-table.js';
import { Drawer } from './ui/drawer.js';
import { HelpDialog, SettingsDialog, StatusDialog } from './ui/dialogs.js';

const PAGE_SIZE = 200;
const MAX_ROWS = 20_000;
const STATUS_INTERVAL = 15_000;
const LIVE_STATS_INTERVAL = 10_000;

// mergeRows puts newer rows first, dropping ids already shown.
function mergeRows(newer, older) {
  const seen = new Set(older.map((r) => r.id));
  return [...newer.filter((r) => !seen.has(r.id)), ...older].slice(0, MAX_ROWS);
}

function App() {
  const initial = useMemo(readURL, []);
  const [query, setQuery] = useState(initial.query);
  const [draft, setDraft] = useState(initial.query);
  const [range, setRange] = useState(initial.range);
  const [lastPreset, setLastPreset] = useState(initial.range.key === 'custom' ? 'all' : initial.range.key);
  const [live, setLive] = useState(initial.live);
  const [prefs, setPrefs] = useState(loadPrefs);

  const [results, setResults] = useState(null);
  const [queryError, setQueryError] = useState(null);
  const [fetchError, setFetchError] = useState(null);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);

  const [selected, setSelected] = useState(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [reveal, setReveal] = useState(null);
  const [pending, setPending] = useState([]);
  const [liveState, setLiveState] = useState('off');
  const [dialog, setDialog] = useState(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [status, setStatus] = useState(null);
  const [traceFields, setTraceFields] = useState([]);
  const [traceRestore, setTraceRestore] = useState(null);

  const inputRef = useRef(null);
  const atTop = useRef(true);
  const holdLive = useRef(false);
  holdLive.current = drawerOpen;

  useEffect(() => writeURL({ query, range, live }), [query, range, live]);
  useEffect(() => savePrefs(prefs), [prefs]);

  // First page: rows, total, histogram, and facets. A newer request
  // aborts the older one, so results always match the query shown.
  useEffect(() => {
    const controller = new AbortController();
    const params = { q: query, ...rangeParams(range) };
    setLoading(true);
    getJSON('/api/query', { ...params, limit: PAGE_SIZE }, controller.signal)
      .then((body) => {
        setResults((prev) => ({
          seq: (prev?.seq || 0) + 1,
          query,
          params,
          rows: body.rows,
          nextCursor: body.next_cursor || null,
          total: body.total,
          histogram: body.histogram,
          facets: body.facets,
          highlight: body.highlight || [],
          maxId: body.max_id,
        }));
        setQueryError(null);
        setFetchError(null);
        setPending([]);
      })
      .catch((err) => {
        if (isAbort(err)) {
          return;
        }
        if (err instanceof APIError && err.status === 400) {
          setQueryError({ message: err.message, pos: err.pos });
        } else {
          setFetchError(`Search failed: ${err.message}`);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [query, range, refreshKey]);

  const loadMore = useCallback(() => {
    if (!results?.nextCursor || loadingMore || results.rows.length >= MAX_ROWS) {
      return;
    }
    const seq = results.seq;
    setLoadingMore(true);
    getJSON('/api/query', { ...results.params, limit: PAGE_SIZE, cursor: results.nextCursor })
      .then((body) => setResults((r) => (r?.seq === seq
        ? { ...r, rows: [...r.rows, ...body.rows].slice(0, MAX_ROWS), nextCursor: body.next_cursor || null }
        : r)))
      .catch((err) => setFetchError(`Loading more failed: ${err.message}`))
      .finally(() => setLoadingMore(false));
  }, [results, loadingMore]);

  // Live updates stream rows newer than the first page. They go straight
  // to the top while the reader is there; otherwise they wait behind the
  // "new lines" pill so nothing shifts under the reader.
  useEffect(() => {
    if (!live || !results) {
      setLiveState('off');
      return undefined;
    }
    const seq = results.seq;
    const source = new EventSource('/api/live?' + new URLSearchParams({ q: results.query, after: String(results.maxId) }));
    setLiveState('connecting');
    source.onopen = () => setLiveState('open');
    source.onerror = () => setLiveState(source.readyState === EventSource.CLOSED ? 'closed' : 'retrying');
    source.addEventListener('rows', (event) => {
      const incoming = JSON.parse(event.data).reverse();
      if (atTop.current && !holdLive.current) {
        setResults((r) => (r?.seq === seq ? { ...r, rows: mergeRows(incoming, r.rows) } : r));
      } else {
        setPending((p) => [...incoming, ...p].slice(0, MAX_ROWS));
      }
    });
    return () => source.close();
  }, [live, results?.seq]);

  // While live, refresh the counts, histogram, and facets now and then.
  useEffect(() => {
    if (!live || !results) {
      return undefined;
    }
    const seq = results.seq;
    const timer = setInterval(() => {
      getJSON('/api/query', { q: results.query, ...rangeParams(range), limit: 1 })
        .then((body) => setResults((r) => (r?.seq === seq ? { ...r, total: body.total, histogram: body.histogram, facets: body.facets } : r)))
        .catch((err) => console.warn('refresh counts:', err.message));
    }, LIVE_STATS_INTERVAL);
    return () => clearInterval(timer);
  }, [live, results?.seq, range]);

  const loadStatus = useCallback(() => {
    getJSON('/api/status').then(setStatus).catch((err) => console.warn('status:', err.message));
  }, []);
  useEffect(() => {
    loadStatus();
    const timer = setInterval(loadStatus, STATUS_INTERVAL);
    return () => clearInterval(timer);
  }, []);

  const loadSettings = useCallback(() => {
    getJSON('/api/settings').then((body) => setTraceFields(body.trace_fields || [])).catch((err) => console.warn('settings:', err.message));
  }, []);
  useEffect(loadSettings, []);

  function commit(value) {
    if (value === query) {
      setRefreshKey((k) => k + 1);
    } else {
      setQuery(value);
    }
  }

  function applyQuery(value) {
    setDraft(value);
    setQuery(value);
  }

  async function rewrite(path, params) {
    try {
      const body = await getJSON(path, params);
      applyQuery(body.q);
      return body.q;
    } catch (err) {
      if (err instanceof APIError && err.status === 400 && err.pos !== undefined) {
        setQueryError({ message: err.message, pos: err.pos });
      } else {
        setFetchError(err.message);
      }
      return null;
    }
  }

  function selectFacet(field, sel) {
    const params = new URLSearchParams({ q: query, field, mode: sel.mode });
    sel.values.forEach((v) => params.append('value', v));
    rewrite('/api/query/select', params);
  }

  function filterBy(field, value, exclude) {
    rewrite('/api/query/filter', { q: query, field, value, exclude: exclude ? '1' : '' });
  }

  async function toggleTrace(field, value) {
    if (traceRestore && query === traceRestore.applied) {
      applyQuery(traceRestore.previous);
      setTraceRestore(null);
      return;
    }
    const previous = query;
    const applied = await rewrite('/api/query/filter', { q: '', field, value });
    if (applied !== null) {
      setTraceRestore({ previous, applied });
    }
  }

  function chooseRange(key) {
    setRange({ key });
    setLastPreset(key);
  }

  function zoom(from, to) {
    setRange({ key: 'custom', from: Math.floor(from), to: Math.ceil(to) });
    setLive(false);
  }

  // Live mode follows the present, so a selected past range gives way to
  // the last preset.
  const toggleLive = useCallback(() => {
    if (!live && range.key === 'custom') {
      setRange({ key: lastPreset });
    }
    setLive(!live);
  }, [live, range, lastPreset]);

  function showPending() {
    setResults((r) => (r ? { ...r, rows: mergeRows(pending, r.rows) } : r));
    setPending([]);
  }

  function select(row) {
    setSelected(row);
    setReveal(row.id);
  }

  async function clearLogs() {
    setMenuOpen(false);
    if (!window.confirm('Delete every stored log line? Ingestion keeps running and stores new lines.')) {
      return;
    }
    try {
      await postJSON('/api/clear');
      setSelected(null);
      setDrawerOpen(false);
      setRefreshKey((k) => k + 1);
      loadStatus();
    } catch (err) {
      setFetchError(`Clearing logs failed: ${err.message}`);
    }
  }

  // Keyboard: / to search, j/k to move, Enter to open, Esc to close, l for
  // live, ? for help. Keys typed into fields and dialogs are left alone.
  useEffect(() => {
    function onKeyDown(event) {
      if (document.querySelector('dialog[open]')) {
        return;
      }
      if (event.key === 'Escape') {
        if (menuOpen) {
          setMenuOpen(false);
        } else if (drawerOpen) {
          setDrawerOpen(false);
        }
        return;
      }
      const typing = event.target.closest?.('input, textarea, select, [contenteditable]');
      if (typing || event.metaKey || event.ctrlKey || event.altKey) {
        return;
      }
      const rows = results?.rows || [];
      const index = selected ? rows.findIndex((r) => r.id === selected.id) : -1;
      switch (event.key) {
        case '/':
          event.preventDefault();
          inputRef.current?.focus();
          inputRef.current?.select();
          break;
        case 'j':
        case 'k': {
          event.preventDefault();
          if (rows.length === 0) {
            break;
          }
          const step = event.key === 'j' ? 1 : -1;
          const next = index < 0 ? 0 : Math.min(Math.max(index + step, 0), rows.length - 1);
          select(rows[next]);
          if (next >= rows.length - 20) {
            loadMore();
          }
          break;
        }
        case 'Enter':
          if (selected) {
            event.preventDefault();
            setDrawerOpen(true);
          }
          break;
        case 'l':
          toggleLive();
          break;
        case '?':
          setDialog('help');
          break;
        default:
      }
    }
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [results, selected, drawerOpen, menuOpen, toggleLive, loadMore]);

  useEffect(() => {
    if (!menuOpen) {
      return undefined;
    }
    const close = (event) => !event.target.closest('.menu') && setMenuOpen(false);
    document.addEventListener('click', close);
    return () => document.removeEventListener('click', close);
  }, [menuOpen]);

  const rows = results?.rows || [];
  const nomadError = status?.nomad_error;
  const traceActive = Boolean(traceRestore && query === traceRestore.applied);

  return html`
    <div class="app">
      <header class="topbar">
        <a class="brand" href="/" title="nomadl">nomadl</a>
        <${QueryBar} draft=${draft} setDraft=${setDraft} onCommit=${commit} error=${queryError} inputRef=${inputRef} />
        <select class="range" value=${range.key} onChange=${(e) => chooseRange(e.currentTarget.value)} aria-label="Time range">
          ${range.key === 'custom' && html`<option value="custom">Selected range</option>`}
          ${RANGES.map((r) => html`<option value=${r.key}>${r.label}</option>`)}
        </select>
        <button type="button" class="button live-toggle ${live ? 'on' : ''} live-${liveState}" aria-pressed=${live}
          title=${live ? 'Stop live updates (l)' : 'Show new lines as they arrive (l)'} onClick=${toggleLive}>
          <span class="live-dot" aria-hidden="true"></span>${liveState === 'retrying' ? 'Reconnecting' : 'Live'}
        </button>
        <${Health} status=${status} onOpen=${() => setDialog('status')} />
        <div class="menu">
          <button type="button" class="icon-button menu-button" aria-label="Menu" aria-expanded=${menuOpen} onClick=${() => setMenuOpen(!menuOpen)}>⋯</button>
          ${menuOpen && html`
            <div class="menu-panel" role="menu">
              <button type="button" role="menuitem" onClick=${() => { setMenuOpen(false); setDialog('settings'); }}>Settings…</button>
              <button type="button" role="menuitem" onClick=${() => { setMenuOpen(false); setDialog('status'); }}>Ingest status…</button>
              <button type="button" role="menuitem" onClick=${() => { setMenuOpen(false); setDialog('help'); }}>Keyboard and search help</button>
              <hr />
              <span class="menu-label">Columns</span>
              ${['level', 'service', 'task'].map((col) => html`
                <label class="menu-check" role="menuitemcheckbox" aria-checked=${prefs.columns[col]}>
                  <input type="checkbox" checked=${prefs.columns[col]}
                    onChange=${(e) => setPrefs({ ...prefs, columns: { ...prefs.columns, [col]: e.currentTarget.checked } })} />
                  ${col[0].toUpperCase() + col.slice(1)}
                </label>
              `)}
              <label class="menu-check" role="menuitemcheckbox" aria-checked=${prefs.utc}>
                <input type="checkbox" checked=${prefs.utc} onChange=${(e) => setPrefs({ ...prefs, utc: e.currentTarget.checked })} />
                Times in UTC
              </label>
              <hr />
              <button type="button" role="menuitem" class="danger" onClick=${clearLogs}>Delete stored logs…</button>
            </div>
          `}
        </div>
      </header>
      ${fetchError && html`
        <div class="banner error" role="alert">
          <span>${fetchError}</span>
          <button type="button" class="link-button" onClick=${() => { setFetchError(null); setRefreshKey((k) => k + 1); }}>Retry</button>
        </div>
      `}
      ${nomadError && html`
        <div class="banner warn" role="status">
          <span>Can't reach Nomad at <code>${status.nomad_addr}</code>. Showing stored logs; ingestion resumes when Nomad is back.</span>
          <span class="dim">${nomadError}</span>
        </div>
      `}
      <div class="workspace ${drawerOpen && selected ? 'with-drawer' : ''}">
        <${Sidebar} facets=${results?.facets} onSelect=${selectFacet} nomadReachable=${Boolean(status) && !nomadError} />
        <main class="results ${loading ? 'loading' : ''}">
          <${Histogram} histogram=${results?.histogram} total=${results?.total} utc=${prefs.utc}
            onZoom=${zoom} zoomed=${range.key === 'custom'} onClearZoom=${() => chooseRange(lastPreset)} />
          <${LogTable}
            rows=${rows}
            highlight=${results?.highlight}
            prefs=${prefs}
            selectedId=${selected?.id}
            onSelect=${select}
            onOpen=${() => setDrawerOpen(true)}
            onNearEnd=${loadMore}
            onScrollTop=${(top) => { atTop.current = top; }}
            pending=${pending.length}
            onShowPending=${showPending}
            revealId=${reveal}
            resetKey=${results?.seq}
            empty=${html`<${Empty} loading=${loading && !results} query=${query} status=${status} onClear=${() => applyQuery('')} />`}
          />
          <footer class="results-foot">
            ${results && html`<span>${formatCount(rows.length)} shown${results.nextCursor ? ' · scroll for more' : ''}${rows.length >= MAX_ROWS ? ' · limit reached; narrow the search' : ''}</span>`}
            ${loadingMore && html`<span class="dim">Loading more…</span>`}
          </footer>
        </main>
        ${drawerOpen && selected && html`
          <${Drawer} row=${selected} prefs=${prefs} traceFields=${traceFields} traceActive=${traceActive}
            onClose=${() => setDrawerOpen(false)} onFilter=${filterBy} onTrace=${toggleTrace} />
        `}
      </div>
      ${dialog === 'settings' && html`<${SettingsDialog} onClose=${() => setDialog(null)} onSaved=${() => { loadSettings(); loadStatus(); setRefreshKey((k) => k + 1); }} />`}
      ${dialog === 'status' && html`<${StatusDialog} status=${status} onClose=${() => setDialog(null)} />`}
      ${dialog === 'help' && html`<${HelpDialog} onClose=${() => setDialog(null)} />`}
    </div>
  `;
}

// Health summarizes ingestion in the toolbar; it opens the status dialog.
function Health({ status, onOpen }) {
  let tone = 'unknown';
  let label = 'Checking…';
  if (status) {
    const waiting = status.waiting_streams?.length || 0;
    const active = status.active_streams?.length || 0;
    if (status.nomad_error) {
      tone = 'bad';
      label = 'Nomad unreachable';
    } else if (!status.ingest_enabled) {
      tone = 'idle';
      label = 'Ingestion off';
    } else if (waiting > 0) {
      tone = 'warn';
      label = `${active} streams · ${waiting} waiting`;
    } else {
      tone = 'good';
      label = `${active} ${active === 1 ? 'stream' : 'streams'}`;
    }
  }
  return html`
    <button type="button" class="health health-${tone}" onClick=${onOpen} title="Ingest status">
      <span class="health-dot" aria-hidden="true"></span>${label}
    </button>
  `;
}

function Empty({ loading, query, status, onClear }) {
  if (loading) {
    return html`<div class="empty">Loading…</div>`;
  }
  if (query) {
    return html`
      <div class="empty">
        <p>No log lines match this search.</p>
        <button type="button" class="button" onClick=${onClear}>Clear the search</button>
      </div>
    `;
  }
  if (status?.nomad_error) {
    return html`<div class="empty"><p>No stored logs, and Nomad is unreachable.</p></div>`;
  }
  const streams = status?.active_streams?.length || 0;
  return html`
    <div class="empty">
      <p>No log lines stored yet.</p>
      ${streams > 0 && html`<p class="dim">Following ${streams} ${streams === 1 ? 'stream' : 'streams'}; new lines appear here as they're written.</p>`}
    </div>
  `;
}

render(html`<${App} />`, document.getElementById('app'));
