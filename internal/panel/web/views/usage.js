import { h, mount, errorBox, loading, empty, table, pagedTable, field } from '../lib/dom.js';
import { get, isAbort, serverTimezone } from '../lib/api.js';
import { go, href, withQuery } from '../lib/router.js';
import { stackedBars } from '../lib/chart.js';
import {
  fmtTime, fmtInt, fmtCompact, fmtMs, dayIn, addDays, isDay, label, PURPOSE, OUTCOME,
} from '../lib/format.js';

const GROUPS = { model: '模型', role: '角色', purpose: '用途', bot: 'Bot' };
const METRICS = {
  tokens: { title: '总 token', value: (r) => r.prompt_tokens + r.completion_tokens + r.reasoning_tokens },
  calls: { title: '调用次数', value: (r) => r.calls },
  errors: { title: '失败次数', value: (r) => r.errors },
};
const RANGES = ['7', '30', '90'];

const keyLabel = (k) => k || '（空）';

// reportDays turns a usage report into chart input for one of METRICS, with
// keys in the order of the report's totals.
export function reportDays(report, metric) {
  const value = METRICS[metric].value;
  const keys = (report.totals || []).map((r) => keyLabel(r.key));
  const days = (report.days || []).map((d) => {
    const values = {};
    for (const r of d.rows || []) values[keyLabel(r.key)] = value(r);
    return { day: d.day, values };
  });
  return { days, keys };
}

// callColumns are the table columns of model calls.
export function callColumns() {
  return [
    { title: '时间', class: 'nowrap', cell: (c) => fmtTime(c.at) },
    { title: '用途', class: 'nowrap', cell: (c) => label(PURPOSE, c.purpose) },
    { title: '角色', cell: (c) => c.role },
    { title: '模型', cell: (c) => c.model },
    { title: 'Bot', cell: (c) => c.bot_instance_id },
    {
      title: '会话',
      cell: (c) => (c.conversation_key
        ? h('a', { class: 'mono', href: href('conversations', c.conversation_key, 'messages') }, c.conversation_key)
        : '—'),
    },
    {
      title: '任务',
      cell: (c) => (c.task_id ? h('a', { class: 'mono', href: href('tasks', c.task_id) }, c.task_id) : '—'),
    },
    { title: '提示', class: 'num', cell: (c) => fmtInt(c.prompt_tokens) },
    { title: '回复', class: 'num', cell: (c) => fmtInt(c.completion_tokens) },
    { title: '思考', class: 'num', cell: (c) => fmtInt(c.reasoning_tokens) },
    { title: '估算输入', class: 'num', cell: (c) => fmtInt(c.estimated_tokens) },
    { title: '耗时', class: 'num', cell: (c) => fmtMs(c.latency_ms) },
    {
      title: '结果',
      cell: (c) => h('span', { class: c.outcome === 'ok' ? 'badge on' : 'badge bad' }, label(OUTCOME, c.outcome)),
    },
    { title: '结束原因', cell: (c) => c.finish_reason || '—' },
  ];
}

function select(options, current, attrs) {
  return h('select', attrs, Object.entries(options).map(([v, t]) => h('option', { value: v, selected: v === current }, t)));
}

function totalsTable(report, group) {
  const rows = report.totals || [];
  if (!rows.length) return empty('此范围内没有调用');
  return table([
    { title: GROUPS[group], cell: (r) => (group === 'purpose' ? `${label(PURPOSE, r.key)}（${r.key}）` : keyLabel(r.key)) },
    { title: '调用', class: 'num', cell: (r) => fmtInt(r.calls) },
    { title: '失败', class: 'num', cell: (r) => fmtInt(r.errors) },
    { title: '提示 token', class: 'num', cell: (r) => fmtInt(r.prompt_tokens) },
    { title: '回复 token', class: 'num', cell: (r) => fmtInt(r.completion_tokens) },
    { title: '思考 token', class: 'num', cell: (r) => fmtInt(r.reasoning_tokens) },
    { title: '合计 token', class: 'num', cell: (r) => fmtInt(r.prompt_tokens + r.completion_tokens + r.reasoning_tokens) },
    { title: '延迟 p50', class: 'num', cell: (r) => fmtMs(r.latency_p50_ms) },
    { title: '延迟 p95', class: 'num', cell: (r) => fmtMs(r.latency_p95_ms) },
  ], rows);
}

export async function usageView(ctx) {
  const q = ctx.query;
  const { signal } = ctx;
  const tz = await serverTimezone(signal);
  const today = dayIn(tz);
  const range = RANGES.includes(q.get('range')) || q.get('range') === 'custom' ? q.get('range') : '7';
  const group = Object.hasOwn(GROUPS, q.get('group')) ? q.get('group') : 'model';
  const metric = Object.hasOwn(METRICS, q.get('metric')) ? q.get('metric') : 'tokens';
  let from;
  let to;
  if (range === 'custom') {
    to = isDay(q.get('to')) ? q.get('to') : today;
    from = isDay(q.get('from')) ? q.get('from') : addDays(to, -6);
  } else {
    to = today;
    from = addDays(today, -(Number(range) - 1));
  }
  const state = {
    range, from, to, group, metric,
    model: q.get('model') || '',
    purpose: q.get('purpose') || '',
    conversation: q.get('conversation') || '',
  };
  const set = (patch) => {
    const n = { ...state, ...patch };
    if (n.range !== 'custom') {
      n.from = '';
      n.to = '';
    }
    go(withQuery('#/usage', n));
  };

  const fromInput = h('input', { type: 'date', value: from });
  const toInput = h('input', { type: 'date', value: to });
  const toolbar = h('div', { class: 'toolbar' },
    RANGES.map((r) => h('button', {
      type: 'button',
      class: range === r ? 'selected' : null,
      onclick: () => set({ range: r }),
    }, `${r} 天`)),
    fromInput, h('span', { class: 'muted' }, '至'), toInput,
    h('button', {
      type: 'button',
      class: range === 'custom' ? 'selected' : null,
      onclick: () => set({ range: 'custom', from: fromInput.value, to: toInput.value }),
    }, '自定义'),
    h('span', { class: 'spacer' }),
    h('label', { class: 'field inline' }, h('span', { class: 'field-label' }, '分组'),
      select(GROUPS, group, { onchange: (e) => set({ group: e.target.value }) })),
    h('label', { class: 'field inline' }, h('span', { class: 'field-label' }, '指标'),
      select(Object.fromEntries(Object.entries(METRICS).map(([k, m]) => [k, m.title])), metric,
        { onchange: (e) => set({ metric: e.target.value }) })));

  const chartBox = h('div', null, loading());
  const totalsBox = h('div', null, loading());

  const model = h('input', { value: state.model, placeholder: '模型' });
  const purpose = select({ '': '全部用途', ...PURPOSE }, state.purpose);
  const conversation = h('input', { value: state.conversation, placeholder: '会话键' });
  const callFilter = h('form', {
    class: 'toolbar',
    onsubmit: (e) => {
      e.preventDefault();
      set({ model: model.value.trim(), purpose: purpose.value, conversation: conversation.value.trim() });
    },
  }, field('模型', model), field('用途', purpose), field('会话', conversation), h('button', { type: 'submit' }, '筛选'));

  const calls = pagedTable(callColumns(), async (before) => {
    const res = await get('usage/calls', {
      from, to, model: state.model, purpose: state.purpose, conversation: state.conversation, before, limit: 50,
    }, signal);
    return { rows: res.calls || [], next: res.next_before };
  }, '没有符合条件的调用');

  ctx.show(
    h('h1', null, '用量'),
    toolbar,
    h('p', { class: 'hint' }, `${from} 至 ${to}，按 ${tz || '浏览器'} 时区的自然日统计。超过 90 天不计算延迟分位数。`),
    h('section', { class: 'card' }, h('h2', null, `每日${METRICS[metric].title}（按${GROUPS[group]}）`), chartBox),
    h('section', { class: 'card' }, h('h2', null, '合计'), totalsBox),
    h('section', { class: 'card' }, h('h2', null, '调用明细'), callFilter, calls),
  );

  try {
    const report = await get('usage', { from, to, group }, signal);
    const { days, keys } = reportDays(report, metric);
    mount(chartBox, stackedBars({
      days, keys, format: metric === 'tokens' ? fmtCompact : fmtInt, label: `每日${METRICS[metric].title}`,
    }));
    mount(totalsBox, totalsTable(report, group));
  } catch (err) {
    if (isAbort(err)) return;
    mount(chartBox, errorBox(err));
    mount(totalsBox);
  }
}
