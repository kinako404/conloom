// SVG charts.

import { h, s } from './dom.js';

const PALETTE = ['#4e79a7', '#f28e2b', '#59a14f', '#e15759', '#76b7b2',
  '#edc948', '#b07aa1', '#ff9da7', '#9c755f', '#8c8c8c'];

export function color(i) {
  return PALETTE[i % PALETTE.length];
}

function niceMax(v) {
  if (!(v > 0)) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 2.5, 5, 10]) {
    if (m * p >= v) return m * p;
  }
  return 10 * p;
}

// stackedBars draws one stacked bar per day with a legend; hovering a bar
// or a segment shows its values. days: [{day: 'YYYY-MM-DD', values: {key: n}}];
// keys are stacked bottom to top in order; format renders a value.
export function stackedBars({ days, keys, format = String, height = 240, label = '' }) {
  const width = 720;
  const left = 56;
  const right = 8;
  const top = 12;
  const bottom = 26;
  const plotW = width - left - right;
  const plotH = height - top - bottom;
  const totals = days.map((d) => keys.reduce((a, k) => a + (Number(d.values[k]) || 0), 0));
  const max = niceMax(Math.max(0, ...totals));
  const step = plotW / Math.max(1, days.length);
  const barW = Math.max(1, Math.min(40, step * 0.7));
  const y = (v) => top + plotH - (v / max) * plotH;

  const grid = [];
  for (let i = 0; i <= 4; i++) {
    const v = (max * i) / 4;
    grid.push(
      s('line', { class: 'grid', x1: left, x2: width - right, y1: y(v), y2: y(v) }),
      s('text', { class: 'axis', x: left - 6, y: y(v) + 4, 'text-anchor': 'end' }, format(v)),
    );
  }

  const every = Math.max(1, Math.ceil(days.length / 10));
  const bars = days.map((d, i) => {
    const x = left + i * step + (step - barW) / 2;
    let acc = 0;
    const segs = [];
    keys.forEach((k, ki) => {
      const v = Number(d.values[k]) || 0;
      if (v <= 0) return;
      const y0 = y(acc);
      acc += v;
      const y1 = y(acc);
      segs.push(s('rect', { x, y: y1, width: barW, height: Math.max(0.5, y0 - y1), fill: color(ki) },
        s('title', null, `${d.day} ${k}：${format(v)}`)));
    });
    const lines = keys.filter((k) => Number(d.values[k]) > 0).map((k) => `${k}：${format(d.values[k])}`);
    return s('g', { class: 'bar' },
      s('title', null, [`${d.day} 合计 ${format(totals[i])}`, ...lines].join('\n')),
      s('rect', { class: 'hit', x: left + i * step, y: top, width: step, height: plotH }),
      segs,
      i % every === 0 || i === days.length - 1
        ? s('text', { class: 'axis', x: left + i * step + step / 2, y: height - 8, 'text-anchor': 'middle' }, d.day.slice(5))
        : null);
  });

  const noData = totals.every((t) => t === 0)
    ? s('text', { class: 'axis', x: left + plotW / 2, y: top + plotH / 2, 'text-anchor': 'middle' }, '此范围内没有数据')
    : null;

  const svg = s('svg', {
    class: 'chart-svg',
    viewBox: `0 0 ${width} ${height}`,
    preserveAspectRatio: 'xMidYMid meet',
    role: 'img',
    'aria-label': label,
  }, grid, bars, noData);

  const legend = h('ul', { class: 'legend' }, keys.map((k, i) => h('li', null,
    s('svg', { class: 'swatch', width: 12, height: 12, viewBox: '0 0 12 12', 'aria-hidden': 'true' },
      s('rect', { width: 12, height: 12, rx: 2, fill: color(i) })),
    k)));

  return h('figure', { class: 'chart' }, svg, keys.length ? legend : null);
}
