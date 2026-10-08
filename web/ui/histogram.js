// histogram.js draws log volume over time, stacked by level with the most
// severe levels at the bottom so error spikes read from the baseline.
// Dragging across it or clicking a bar zooms into that time range.
import { useRef, useState } from 'preact/hooks';
import { html } from '../lib/html.js';
import { formatClock, formatCount, LEVEL_LABELS, LEVELS } from '../lib/format.js';

const MIN_DRAG_PX = 4;

export function Histogram({ histogram, total, utc, onZoom, zoomed, onClearZoom }) {
  const trackRef = useRef(null);
  const [hover, setHover] = useState(null); // bin index
  const [drag, setDrag] = useState(null); // { from, to } in px from track left, for drawing

  const bins = histogram?.bins || [];
  if (!histogram || bins.length === 0 || !total) {
    return html`<div class="histogram empty"><span class="histogram-caption">No matching logs in this range</span></div>`;
  }

  const start = histogram.start_ms;
  const span = Math.max(histogram.end_ms - start, 1);
  const binSpan = span / bins.length;
  const peak = Math.max(...bins.map((b) => b.count), 1);
  const withDate = span > 24 * 3600 * 1000;
  const errors = bins.reduce((sum, b) => sum + (b.levels?.error || 0) + (b.levels?.emergency || 0), 0);

  function timeAt(px, width) {
    return start + (Math.min(Math.max(px, 0), width) / width) * span;
  }

  function binAt(x, width) {
    return Math.min(bins.length - 1, Math.max(0, Math.floor((x / width) * bins.length)));
  }

  // A drag follows the pointer across the whole document, so releasing
  // outside the track still finishes it.
  function onPointerDown(event) {
    const rect = trackRef.current.getBoundingClientRect();
    const current = { from: event.clientX - rect.left, to: event.clientX - rect.left };
    setDrag({ ...current });

    function move(e) {
      current.to = e.clientX - rect.left;
      setDrag({ ...current });
    }
    function up() {
      document.removeEventListener('pointermove', move);
      document.removeEventListener('pointerup', up);
      setDrag(null);
      const lo = Math.max(Math.min(current.from, current.to), 0);
      const hi = Math.min(Math.max(current.from, current.to), rect.width);
      if (hi - lo >= MIN_DRAG_PX) {
        onZoom(timeAt(lo, rect.width), timeAt(hi, rect.width));
        return;
      }
      const bin = binAt(lo, rect.width);
      if (bins[bin].count > 0) {
        onZoom(start + bin * binSpan, start + (bin + 1) * binSpan);
      }
    }
    document.addEventListener('pointermove', move);
    document.addEventListener('pointerup', up);
  }

  function onPointerMove(event) {
    const rect = trackRef.current.getBoundingClientRect();
    setHover(binAt(event.clientX - rect.left, rect.width));
  }

  const hovered = hover !== null && !drag ? bins[hover] : null;
  return html`
    <div class="histogram">
      <div class="histogram-head">
        <span class="histogram-caption">
          <strong>${formatCount(total)}</strong> matching
          ${errors > 0 && html` · <span class="lvl-text-error">${formatCount(errors)} errors</span>`}
        </span>
        ${hovered && html`
          <span class="histogram-readout">
            ${formatClock(start + hover * binSpan, utc, withDate)}–${formatClock(start + (hover + 1) * binSpan, utc, false)}
            ${' · '}${formatCount(hovered.count)}
            ${LEVELS.filter((l) => hovered.levels?.[l]).map((l) => html` · <span class="lvl-text-${l}">${formatCount(hovered.levels[l])} ${LEVEL_LABELS[l].toLowerCase()}</span>`)}
          </span>
        `}
        ${zoomed && html`<button type="button" class="link-button" onClick=${onClearZoom}>Clear time selection</button>`}
      </div>
      <div
        ref=${trackRef}
        class="histogram-track"
        onPointerDown=${onPointerDown}
        onPointerMove=${onPointerMove}
        onPointerLeave=${() => setHover(null)}
        role="img"
        aria-label="Log volume over time; drag to zoom into a time range"
      >
        ${bins.map((bin, i) => html`
          <div class="bar ${i === hover ? 'hovered' : ''}" style=${{ height: `${(bin.count / peak) * 100}%` }}>
            ${LEVELS.filter((l) => bin.levels?.[l]).map((l) => html`
              <div class="bar-seg lvl-${l}" style=${{ flexGrow: bin.levels[l] }}></div>
            `)}
          </div>
        `)}
        ${drag && Math.abs(drag.to - drag.from) >= MIN_DRAG_PX && html`
          <div class="brush" style=${{ left: `${Math.min(drag.from, drag.to)}px`, width: `${Math.abs(drag.to - drag.from)}px` }}></div>
        `}
      </div>
      <div class="histogram-axis" aria-hidden="true">
        <span>${formatClock(start, utc, withDate)}</span>
        <span>${formatClock(start + span / 2, utc, withDate)}</span>
        <span>${formatClock(start + span, utc, withDate)}</span>
      </div>
    </div>
  `;
}
