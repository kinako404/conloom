import { h, pagedTable, field } from '../lib/dom.js';
import { get } from '../lib/api.js';
import { go, withQuery } from '../lib/router.js';
import { fmtTime, pretty } from '../lib/format.js';

// detailNode shows an event's detail: an object as JSON, anything else as text.
function detailNode(d) {
  if (d === null || d === undefined || d === '') return '—';
  return h('pre', { class: 'code' }, typeof d === 'object' ? pretty(d) : String(d));
}

const columns = [
  { title: '时间', class: 'nowrap', cell: (e) => h('span', { title: e.id }, fmtTime(e.created_at)) },
  { title: '种类', cell: (e) => h('span', { class: 'badge' }, e.kind) },
  { title: '操作者', cell: (e) => e.actor },
  { title: 'Bot', cell: (e) => e.bot_instance_id || '—' },
  { title: '对象', cell: (e) => h('span', { class: 'mono' }, e.target || '—') },
  { title: '详情', class: 'text', cell: (e) => detailNode(e.detail) },
];

export function auditView(ctx) {
  const q = ctx.query;
  const filter = { kind: q.get('kind') || '', bot: q.get('bot') || '' };
  const kind = h('input', { value: filter.kind, placeholder: '如 policy.update' });
  const bot = h('input', { value: filter.bot, placeholder: 'bot ID' });
  const form = h('form', {
    class: 'toolbar',
    onsubmit: (e) => {
      e.preventDefault();
      go(withQuery('#/audit', { kind: kind.value.trim(), bot: bot.value.trim() }));
    },
  }, field('种类', kind), field('Bot', bot), h('button', { type: 'submit' }, '筛选'));
  const list = pagedTable(columns, async (before) => {
    const res = await get('audit', { ...filter, before, limit: 50 }, ctx.signal);
    return { rows: res.events || [], next: res.next_before };
  }, '没有审计记录');
  ctx.show(h('h1', null, '审计'), form, list);
}
