// state.js keeps the view in the URL, so searches can be shared and
// survive a reload, and keeps display preferences in localStorage.

export const RANGES = [
  { key: '5m', label: 'Last 5 minutes', minutes: 5 },
  { key: '15m', label: 'Last 15 minutes', minutes: 15 },
  { key: '1h', label: 'Last hour', minutes: 60 },
  { key: '4h', label: 'Last 4 hours', minutes: 240 },
  { key: '24h', label: 'Last 24 hours', minutes: 1440 },
  { key: 'all', label: 'All stored logs' },
];

const DEFAULT_RANGE = 'all';

// readURL returns { query, range, live } from the location. range is
// { key } for a preset or { key: 'custom', from, to } in epoch ms. Live
// updates are on unless the URL says live=0 or picks a custom range,
// which is a fixed slice of the past.
export function readURL() {
  const params = new URLSearchParams(location.search);
  const from = Date.parse(params.get('from') || '');
  const to = Date.parse(params.get('to') || '');
  let range = { key: DEFAULT_RANGE };
  if (Number.isFinite(from) && Number.isFinite(to) && from < to) {
    range = { key: 'custom', from, to };
  } else if (RANGES.some((r) => r.key === params.get('range'))) {
    range = { key: params.get('range') };
  }
  return { query: params.get('q') || '', range, live: range.key !== 'custom' && params.get('live') !== '0' };
}

export function writeURL({ query, range, live }) {
  const params = new URLSearchParams();
  if (query) {
    params.set('q', query);
  }
  if (range.key === 'custom') {
    params.set('from', new Date(range.from).toISOString());
    params.set('to', new Date(range.to).toISOString());
  } else if (range.key !== DEFAULT_RANGE) {
    params.set('range', range.key);
  }
  if (!live && range.key !== 'custom') {
    params.set('live', '0');
  }
  const search = params.toString();
  const url = location.pathname + (search ? '?' + search : '');
  if (url !== location.pathname + location.search) {
    history.replaceState(null, '', url);
  }
}

// rangeParams turns a range into the since/until request parameters,
// resolving presets against the current time.
export function rangeParams(range) {
  if (range.key === 'custom') {
    return { since: new Date(range.from).toISOString(), until: new Date(range.to).toISOString() };
  }
  const preset = RANGES.find((r) => r.key === range.key);
  if (!preset?.minutes) {
    return {};
  }
  const now = Date.now();
  return { since: new Date(now - preset.minutes * 60_000).toISOString(), until: new Date(now).toISOString() };
}

const PREFS_KEY = 'nomadl.prefs';
const DEFAULT_PREFS = { utc: false, columns: { level: true, service: true, task: true } };

export function loadPrefs() {
  try {
    const saved = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
    return { ...DEFAULT_PREFS, ...saved, columns: { ...DEFAULT_PREFS.columns, ...saved.columns } };
  } catch {
    return DEFAULT_PREFS;
  }
}

export function savePrefs(prefs) {
  localStorage.setItem(PREFS_KEY, JSON.stringify(prefs));
}
