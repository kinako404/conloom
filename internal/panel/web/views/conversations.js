import { h, mount, empty, table } from '../lib/dom.js';
import { get } from '../lib/api.js';
import { go, href } from '../lib/router.js';
import { fmtTime, fmtInt, label, CONV_TYPE, PRIVATE_LEVEL } from '../lib/format.js';
import { newChatDialog } from './chats.js';

// proactiveBadge shows whether the bot speaks unprompted: in a private chat
// the level it starts the chat at, off when the switch or the level is off.
function proactiveBadge(c) {
  if (c.type === 'private') {
    const on = c.proactive_enabled && c.proactive_level !== 'off';
    return h('span', { class: on ? 'badge on' : 'badge off' }, on ? `主动：${label(PRIVATE_LEVEL, c.proactive_level)}` : '主动关');
  }
  return h('span', { class: c.proactive_enabled ? 'badge on' : 'badge off' }, c.proactive_enabled ? '主动发言开' : '主动发言关');
}

// statusBadges shows whether a conversation is paused and whether proactive
// replies and memory are on.
export function statusBadges(c) {
  return h('span', { class: 'badges' },
    c.paused ? h('span', { class: 'badge bad' }, '已暂停') : h('span', { class: 'badge on' }, '运行中'),
    proactiveBadge(c),
    h('span', { class: c.memory_enabled ? 'badge on' : 'badge off' }, c.memory_enabled ? '记忆开' : '记忆关'));
}

export function personaLabel(c) {
  if (!c.persona_version) return h('span', { class: 'muted' }, '未生成');
  return `${c.persona_template || '自定义'} · 第 ${c.persona_version} 版`;
}

export async function conversationsView(ctx) {
  const res = await get('conversations', null, ctx.signal);
  const all = res.conversations || [];
  const filter = h('input', { type: 'search', placeholder: '按会话键、bot 或平台 ID 筛选' });
  const list = h('div');
  const columns = [
    {
      title: '会话',
      cell: (c) => h('a', { href: href('conversations', c.key) },
        h('div', null, `${label(CONV_TYPE, c.type)} ${c.platform_conversation_id}`),
        h('div', { class: 'mono muted' }, c.key)),
    },
    { title: 'Bot', cell: (c) => [h('div', null, c.bot_name || c.bot_instance_id), h('div', { class: 'mono muted' }, c.bot_instance_id)] },
    { title: '平台', cell: (c) => c.platform },
    { title: '消息数', class: 'num', cell: (c) => fmtInt(c.message_count) },
    { title: '最后消息', class: 'nowrap', cell: (c) => fmtTime(c.last_message_at) },
    { title: '状态', cell: statusBadges },
    { title: '人设', cell: personaLabel },
  ];
  const show = () => {
    const needle = filter.value.trim().toLowerCase();
    const rows = needle
      ? all.filter((c) => [c.key, c.bot_name, c.bot_instance_id, c.platform_conversation_id]
        .some((v) => String(v || '').toLowerCase().includes(needle)))
      : all;
    mount(list, rows.length ? table(columns, rows) : empty(all.length ? '没有匹配的会话' : '还没有会话'));
  };
  filter.addEventListener('input', show);
  show();
  const bots = res.bots || [];
  const chat = newChatDialog((key) => go(href('chat', key)));
  const newChat = h('button', {
    type: 'button',
    disabled: bots.length === 0,
    title: bots.length ? null : '没有 bot',
    onclick: () => chat.open(bots),
  }, '新建聊天');
  ctx.show(h('h1', null, '会话'),
    h('div', { class: 'toolbar' }, filter, h('span', { class: 'muted' }, `共 ${all.length} 个`), h('span', { class: 'spacer' }), newChat),
    list, chat.dialog);
}
