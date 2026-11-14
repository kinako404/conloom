import { h, mount, empty, table, field, errorBox } from '../lib/dom.js';
import { get, post, isAbort } from '../lib/api.js';
import { go, href } from '../lib/router.js';
import { fmtTime, fmtInt, label, CONV_TYPE } from '../lib/format.js';

// statusBadges shows whether a conversation is paused and whether proactive
// replies and memory are on.
export function statusBadges(c) {
  return h('span', { class: 'badges' },
    c.paused ? h('span', { class: 'badge bad' }, '已暂停') : h('span', { class: 'badge on' }, '运行中'),
    h('span', { class: c.proactive_enabled ? 'badge on' : 'badge off' }, c.proactive_enabled ? '主动发言开' : '主动发言关'),
    h('span', { class: c.memory_enabled ? 'badge on' : 'badge off' }, c.memory_enabled ? '记忆开' : '记忆关'));
}

export function personaLabel(c) {
  if (!c.persona_version) return h('span', { class: 'muted' }, '未生成');
  return `${c.persona_template || '自定义'} · 第 ${c.persona_version} 版`;
}

// MAX_DISPLAY_NAME is the longest name a panel chat calls its user by.
const MAX_DISPLAY_NAME = 32;

// newChatDialog asks for a bot and a name to be called, starts a panel chat
// and opens it.
function newChatDialog(bots) {
  const bot = h('select', { required: true }, bots.map((b) => h('option', { value: b.id },
    b.name ? `${b.name}（${b.id}）` : b.id)));
  const name = h('input', { maxlength: MAX_DISPLAY_NAME, placeholder: '留空时用面板用户名' });
  const msg = h('div');
  const create = h('button', { type: 'submit', class: 'primary' }, '开始聊天');
  const dialog = h('dialog', { class: 'card dialog' });
  const cancel = h('button', { type: 'button', onclick: () => dialog.close() }, '取消');
  mount(dialog, h('form', {
    onsubmit: async (e) => {
      e.preventDefault();
      create.disabled = true;
      mount(msg);
      try {
        const res = await post('chats', { bot_instance_id: bot.value, display_name: name.value.trim() });
        dialog.close();
        go(href('conversations', res.key, 'chat'));
      } catch (err) {
        if (!isAbort(err)) mount(msg, errorBox(err));
      } finally {
        create.disabled = false;
      }
    },
  },
  h('h2', null, '新建聊天'),
  h('p', { class: 'hint' }, '在面板里和 bot 私聊。你是这个聊天的管理员，私聊白名单对它不生效。'),
  field('Bot', bot),
  field('称呼', name, `bot 怎么称呼你，最多 ${MAX_DISPLAY_NAME} 字`),
  msg,
  h('div', { class: 'toolbar' }, create, cancel)));
  return dialog;
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
  const dialog = newChatDialog(bots);
  const newChat = h('button', {
    type: 'button',
    disabled: bots.length === 0,
    title: bots.length ? null : '没有配置 bot',
    onclick: () => dialog.showModal(),
  }, '新建聊天');
  ctx.show(h('h1', null, '会话'),
    h('div', { class: 'toolbar' }, filter, h('span', { class: 'muted' }, `共 ${all.length} 个`), h('span', { class: 'spacer' }), newChat),
    list, dialog);
}
