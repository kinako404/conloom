import { h, empty, table, pagedTable, field, kv } from '../lib/dom.js';
import { get } from '../lib/api.js';
import { go, href, withQuery } from '../lib/router.js';
import { fmtTime, label, TASK_STATUS } from '../lib/format.js';
import { callColumns } from './usage.js';

function statusBadge(status) {
  const cls = { succeeded: 'badge on', failed: 'badge bad', cancelled: 'badge', running: 'badge warn', queued: 'badge warn' };
  return h('span', { class: cls[status] || 'badge' }, label(TASK_STATUS, status));
}

function convLink(key) {
  return key ? h('a', { class: 'mono', href: href('conversations', key, 'messages') }, key) : '—';
}

const columns = [
  { title: '创建时间', class: 'nowrap', cell: (t) => fmtTime(t.created_at) },
  { title: '任务', cell: (t) => h('a', { class: 'mono', href: href('tasks', t.id) }, t.id) },
  { title: '状态', cell: (t) => statusBadge(t.status) },
  { title: '会话', cell: (t) => convLink(t.conversation_key) },
  { title: '触发', cell: (t) => t.trigger || '—' },
  { title: '命令', cell: (t) => t.command || '—' },
  { title: '优先级', cell: (t) => t.priority || '—' },
  { title: '模型', cell: (t) => [t.model_role, t.model].filter(Boolean).join(' / ') || '—' },
  { title: '错误', cell: (t) => (t.error_code ? h('span', { class: 'badge bad' }, t.error_code) : '—') },
];

// taskList pages through the tasks matching filter ({status, bot, conversation}).
export function taskList(signal, filter) {
  return pagedTable(columns, async (before) => {
    const res = await get('tasks', { ...filter, before, limit: 50 }, signal);
    return { rows: res.tasks || [], next: res.next_before };
  }, '没有任务（任务只保留一段时间）');
}

export function tasksView(ctx) {
  const q = ctx.query;
  const filter = { status: q.get('status') || '', bot: q.get('bot') || '', conversation: q.get('conversation') || '' };
  const status = h('select', null, h('option', { value: '' }, '全部状态'),
    Object.entries(TASK_STATUS).map(([v, t]) => h('option', { value: v, selected: v === filter.status }, t)));
  const bot = h('input', { value: filter.bot, placeholder: 'bot ID' });
  const conversation = h('input', { value: filter.conversation, placeholder: '会话键' });
  const form = h('form', {
    class: 'toolbar',
    onsubmit: (e) => {
      e.preventDefault();
      go(withQuery('#/tasks', { status: status.value, bot: bot.value.trim(), conversation: conversation.value.trim() }));
    },
  }, field('状态', status), field('Bot', bot), field('会话', conversation), h('button', { type: 'submit' }, '筛选'));
  ctx.show(h('h1', null, '任务'), form, taskList(ctx.signal, filter));
}

function actionItem(a) {
  return h('li', null,
    h('div', { class: 'meta' },
      h('span', null, fmtTime(a.created_at)),
      h('span', { class: 'badge' }, a.type),
      h('span', { class: 'mono' }, a.action_id),
      a.reply_to_message_id ? h('span', null, `回复 ${a.reply_to_message_id}`) : null),
    h('div', { class: 'body' }, (a.content && a.content.text) || ''));
}

export async function taskDetailView(ctx) {
  const id = ctx.params.id;
  const res = await get('tasks/' + encodeURIComponent(id), null, ctx.signal);
  const t = res.task || {};
  const meta = res.meta || {};
  const actions = res.actions || [];
  const calls = res.model_calls || [];
  ctx.show(
    h('p', null, h('a', { href: '#/tasks' }, '← 任务列表')),
    h('h1', null, '任务 ', h('span', { class: 'mono' }, t.id || id)),
    h('div', { class: 'cols' },
      h('section', { class: 'card' }, h('h2', null, '任务'), kv([
        ['状态', statusBadge(t.status)],
        ['Bot', t.bot_instance_id],
        ['会话', convLink(t.conversation_key)],
        ['触发', t.trigger || '—'],
        ['命令', t.command || '—'],
        ['优先级', t.priority || '—'],
        ['模型角色', t.model_role || '—'],
        ['模型', t.model || '—'],
        ['错误', t.error_code || '—'],
        ['创建时间', fmtTime(t.created_at)],
        ['更新时间', fmtTime(t.updated_at)],
      ])),
      h('section', { class: 'card' }, h('h2', null, '元数据'),
        Object.keys(meta).length ? kv(Object.entries(meta).map(([k, v]) => [k, typeof v === 'object' ? JSON.stringify(v) : String(v)])) : empty())),
    h('section', { class: 'card' }, h('h2', null, `动作（${actions.length}）`),
      actions.length ? h('ul', { class: 'messages' }, actions.map(actionItem)) : empty('没有动作')),
    h('section', { class: 'card' }, h('h2', null, `模型调用（${calls.length}）`),
      calls.length ? table(callColumns(), calls) : empty('没有模型调用记录')),
  );
}
