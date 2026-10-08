// format.js holds display helpers shared by the components.

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

function pad(value, width = 2) {
  return String(value).padStart(width, '0');
}

function parts(ms, utc) {
  const d = new Date(ms);
  return utc
    ? { mo: d.getUTCMonth(), day: d.getUTCDate(), h: d.getUTCHours(), m: d.getUTCMinutes(), s: d.getUTCSeconds(), ms: d.getUTCMilliseconds() }
    : { mo: d.getMonth(), day: d.getDate(), h: d.getHours(), m: d.getMinutes(), s: d.getSeconds(), ms: d.getMilliseconds() };
}

// formatTime renders a row timestamp like "Oct 08 14:07:29.123".
export function formatTime(ms, utc) {
  const p = parts(ms, utc);
  return `${MONTHS[p.mo]} ${pad(p.day)} ${pad(p.h)}:${pad(p.m)}:${pad(p.s)}.${pad(p.ms, 3)}`;
}

// formatClock renders an axis label: the time of day, with the date when
// the span covers more than a day.
export function formatClock(ms, utc, withDate) {
  const p = parts(ms, utc);
  const clock = `${pad(p.h)}:${pad(p.m)}:${pad(p.s)}`;
  return withDate ? `${MONTHS[p.mo]} ${pad(p.day)} ${clock}` : clock;
}

export function formatCount(n) {
  return Number(n || 0).toLocaleString();
}

export function shortID(id) {
  return id ? id.slice(0, 8) : '';
}

const encoder = new TextEncoder();

// charIndex converts a byte offset reported by the server into a string
// index, since queries may contain multi-byte characters.
export function charIndex(text, byteOffset) {
  let bytes = 0;
  for (let i = 0; i < text.length;) {
    const ch = String.fromCodePoint(text.codePointAt(i));
    const size = encoder.encode(ch).length;
    if (bytes + size > byteOffset) {
      return i;
    }
    bytes += size;
    i += ch.length;
  }
  return text.length;
}

export function byteOffset(text, index) {
  return encoder.encode(text.slice(0, index)).length;
}

export const LEVELS = ['emergency', 'error', 'warn', 'notice', 'info', 'debug', 'ok'];

export const LEVEL_LABELS = {
  emergency: 'Emergency',
  error: 'Error',
  warn: 'Warn',
  notice: 'Notice',
  info: 'Info',
  debug: 'Debug',
  ok: 'Other',
};
