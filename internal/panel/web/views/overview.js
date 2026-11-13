import { h, mount, errorBox, loading, empty, table, kv } from '../lib/dom.js';
import { get, isAbort, noteTimezone } from '../lib/api.js';
import { stackedBars } from '../lib/chart.js';
import { fmtTime, fmtAgo, fmtInt, fmtCompact, fmtNumber, dayIn, addDays } from '../lib/format.js';
import { reportDays } from './usage.js';

const POLL_MS = 10000;

function stat(k, v, bad) {
  return h('div', { class: bad ? 'stat bad' : 'stat' }, h('div', { class: 'k' }, k), h('div', { class: 'v' }, v));
}

function orDash(v, none) {
  return v === null || v === undefined ? (none || '—') : fmtInt(v);
}

function renderOverview(o) {
  const t = o.today || {};
  const live = o.live || {};
  const breakers = live.breakers_open || [];
  const bots = o.bots || [];
  return [
    h('h2', null, '今日用量'),
    h('div', { class: 'cards' },
      stat('调用次数', fmtInt(t.calls)),
      stat('失败', fmtInt(t.errors), t.errors > 0),
      stat('提示 token', fmtInt(t.prompt_tokens)),
      stat('回复 token', fmtInt(t.completion_tokens)),
      stat('思考 token', fmtInt(t.reasoning_tokens))),
    h('div', { class: 'cols' },
      h('section', { class: 'card' },
        h('h2', null, '实时状态'),
        kv([
          ['队列深度', orDash(live.queue_depth)],
          ['发件箱待发', orDash(live.outbox_pending)],
          ['向量积压', orDash(live.embedding_backlog, '未启用')],
          ['关键词索引补建剩余', orDash(live.fts_backfill_remaining)],
          ['熔断中的模型角色', breakers.length
            ? breakers.map((r) => h('span', { class: 'badge bad' }, r))
            : h('span', { class: 'badge on' }, '无')],
        ])),
      h('section', { class: 'card' },
        h('h2', null, '服务'),
        kv([
          ['版本', o.version || '—'],
          ['提交', h('span', { class: 'mono' }, o.commit || '—')],
          ['启动时间', fmtTime(o.started_at)],
          ['时区', o.timezone || '—'],
        ]))),
    h('section', { class: 'card' },
      h('h2', null, 'Bot'),
      h('p', { class: 'hint' }, '最近调用是适配器最近一次调用 core 的时间，自本次启动起记录。'),
      bots.length
        ? table([
          { title: 'ID', cell: (b) => h('span', { class: 'mono' }, b.id) },
          { title: '名称', cell: (b) => b.name },
          { title: '平台', cell: (b) => b.platform },
          {
            title: '最近调用',
            cell: (b) => (b.last_seen_at
              ? h('span', { title: fmtTime(b.last_seen_at) }, fmtAgo(b.last_seen_at))
              : h('span', { class: 'muted' }, '自本次启动未调用')),
          },
        ], bots)
        : empty('没有配置 bot')),
    h('p', { class: 'hint' }, `每 ${POLL_MS / 1000} 秒自动刷新，更新于 ${fmtTime(new Date().toISOString())}`),
  ];
}

function tokenChart(report) {
  const { days, keys } = reportDays(report, 'tokens');
  return stackedBars({ days, keys, format: fmtCompact, label: '最近 7 天 token 用量' });
}

function metricsTable(data) {
  const rows = [];
  for (const m of data.metrics || []) {
    for (const s of m.series || []) rows.push({ m, s });
  }
  if (!rows.length) return empty('没有指标');
  return table([
    { title: '指标', cell: (r) => h('span', { class: 'mono', title: r.m.help || '' }, r.m.name) },
    {
      title: '标签',
      cell: (r) => h('span', { class: 'mono' },
        Object.entries(r.s.labels || {}).map(([k, v]) => `${k}=${v}`).join(', ')),
    },
    {
      title: '值',
      class: 'num',
      cell: (r) => (r.m.type === 'histogram'
        ? `次数 ${fmtInt(r.s.count)}，总和 ${fmtNumber(r.s.sum)}，p50 ${fmtNumber(r.s.p50)}，p95 ${fmtNumber(r.s.p95)}`
        : fmtNumber(r.s.value)),
    },
  ], rows);
}

export async function overviewView(ctx) {
  const { signal } = ctx;
  const live = h('div', null, loading());
  const pollError = h('div');
  const chartBox = h('div', null, loading());
  const metricsBox = h('div', null, loading());
  const metrics = h('details', { class: 'card' }, h('summary', null, '进程指标（自本次启动）'), metricsBox);

  async function loadMetrics() {
    try {
      mount(metricsBox, metricsTable(await get('metrics/live', null, signal)));
    } catch (err) {
      if (!isAbort(err)) mount(metricsBox, errorBox(err));
    }
  }
  metrics.addEventListener('toggle', () => {
    if (metrics.open) loadMetrics();
  });

  async function refresh() {
    const o = await get('overview', null, signal);
    noteTimezone(o.timezone);
    if (!signal.aborted) mount(live, renderOverview(o));
    return o;
  }

  const o = await refresh();
  ctx.show(h('h1', null, '概览'), pollError, live,
    h('section', { class: 'card' }, h('h2', null, '最近 7 天 token 用量（按模型）'), chartBox),
    metrics);

  const timer = setInterval(async () => {
    try {
      await refresh();
      mount(pollError);
      if (metrics.open) loadMetrics();
    } catch (err) {
      if (!isAbort(err)) mount(pollError, errorBox(err));
    }
  }, POLL_MS);
  signal.addEventListener('abort', () => clearInterval(timer));

  try {
    const to = dayIn(o.timezone);
    const report = await get('usage', { from: addDays(to, -6), to, group: 'model' }, signal);
    mount(chartBox, tokenChart(report));
  } catch (err) {
    if (!isAbort(err)) mount(chartBox, errorBox(err));
  }
}
