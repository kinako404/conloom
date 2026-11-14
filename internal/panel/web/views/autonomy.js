import { h, mount, errorBox, empty, table, kv } from '../lib/dom.js';
import { get, put, del, convPath } from '../lib/api.js';
import { href } from '../lib/router.js';
import { fmtTime, label, PRIVATE_LEVEL } from '../lib/format.js';

// TIMER_KIND are the kinds of a private chat's pending wake-ups.
const TIMER_KIND = {
  status_return: '回来说话',
  initiate: '主动找人',
  follow_up: '追问',
};

// BAR_SOURCE are who changed a bar.
const BAR_SOURCE = {
  model: '模型',
  panel: '面板',
  api: 'API',
};

// PANEL is the platform of conversations held in the panel (store.PlatformPanel).
const PANEL = 'panel';

function statusSection(key, status, write) {
  const title = h('h2', null, '当前状态');
  const hint = h('p', { class: 'hint' }, '模型用 set_status 设定的状态，到点后 bot 回来说一句（需要打开主动发言）。只有 bot 启用了 status 技能时才会设定。');
  if (!status) return h('section', { class: 'card' }, title, empty('现在没有状态'), hint);
  const msg = h('div');
  const end = h('button', {
    type: 'button',
    class: 'danger',
    onclick: () => {
      if (!confirm('结束当前状态？回来说话的定时会一起取消。')) return undefined;
      return write(msg, end, () => del(convPath(key, 'autonomy', 'status')), '状态已结束');
    },
  }, '结束状态');
  return h('section', { class: 'card' },
    title,
    kv([['正在', status.activity], ['开始', fmtTime(status.started_at)], ['预计结束', fmtTime(status.until_at)]]),
    hint,
    h('div', { class: 'toolbar' }, end),
    msg);
}

function barEditor(key, bar, msg, write) {
  const value = h('input', { type: 'number', min: 0, max: 100, step: 1, value: bar.value, 'aria-label': `${bar.name}的新值` });
  const reason = h('input', { placeholder: '原因（可选）', 'aria-label': `修改${bar.name}的原因` });
  const save = h('button', { type: 'submit' }, '保存');
  return h('form', {
    class: 'bar-edit',
    onsubmit: (e) => {
      e.preventDefault();
      const v = Number(value.value);
      if (value.value.trim() === '' || !Number.isInteger(v) || v < 0 || v > 100) {
        mount(msg, errorBox({ message: `「${bar.name}」的值必须是 0–100 的整数` }));
        return undefined;
      }
      return write(msg, save,
        () => put(convPath(key, 'autonomy', 'buffs', encodeURIComponent(bar.name)), { value: v, reason: reason.value.trim() }),
        `已把「${bar.name}」设为 ${v}`);
    },
  }, value, reason, save);
}

function barsSection(key, bars, write) {
  const msg = h('div');
  return h('section', { class: 'card' },
    h('h2', null, '状态条'),
    h('p', { class: 'hint' }, '数值 0–100，按人设里的半衰期逐渐回到基准。在这里设的值立即生效，不受模型调整的单次和每日上限约束。只有 bot 启用了 buff 技能时，状态条才会影响回复。'),
    bars.length ? table([
      {
        title: '状态条',
        cell: (b) => [h('div', null, b.name), b.description ? h('div', { class: 'muted small' }, b.description) : null],
      },
      {
        title: '当前',
        class: 'nowrap',
        cell: (b) => [h('meter', { min: 0, max: 100, value: b.value, 'aria-label': b.name }), ` ${b.value}（${b.band}）`],
      },
      { title: '基准', class: 'num', cell: (b) => String(b.baseline) },
      { title: '设为', cell: (b) => barEditor(key, b, msg, write) },
    ], bars) : empty('人设里没有状态条'),
    msg);
}

function eventsSection(events) {
  return h('section', { class: 'card' },
    h('h2', null, '最近变化'),
    h('p', { class: 'hint' }, '最近 50 次状态条变化，新的在前，保留 30 天。'),
    events.length ? table([
      { title: '时间', class: 'nowrap', cell: (e) => fmtTime(e.at) },
      { title: '状态条', cell: (e) => e.name },
      { title: '变化', class: 'nowrap', cell: (e) => `${e.before} → ${e.after}` },
      { title: '原因', class: 'text', cell: (e) => e.reason || '—' },
      { title: '来源', cell: (e) => label(BAR_SOURCE, e.source) },
    ], events) : empty('最近没有变化'));
}

// quietSource tells where the quiet hours in effect come from: the policy,
// the user's messages, or the default when there are too few of them.
function quietSource(pro, priv) {
  if (priv.quiet_start) return '设定';
  return pro.quiet_inferred ? '按作息推算' : '默认，消息不够推算';
}

function proactiveSection(key, conv, pro, priv) {
  const timers = pro.timers || [];
  return h('section', { class: 'card' },
    h('h2', null, '主动找人'),
    conv.platform === PANEL ? h('p', { class: 'hint' }, '面板里新建的聊天不会主动找人和追问。') : null,
    kv([
      ['总开关', pro.enabled
        ? h('span', { class: 'badge on' }, '开')
        : h('span', { class: 'badge off' }, '关（到点不发消息）')],
      ['档位', `${label(PRIVATE_LEVEL, pro.level)}${priv.level ? '' : '（未设置，按默认）'}`],
      ['已发起', `今天 ${pro.sent_today}/${pro.daily_cap}`],
      ['免打扰', `${pro.quiet_start}–${pro.quiet_end}（${quietSource(pro, priv)}，服务器时区）`],
      ['追问额度', `对方再次发言前还能发起 ${pro.unanswered_left} 条`],
      ['定时', timers.length
        ? h('ul', { class: 'plain' }, timers.map((t) => h('li', null, `${label(TIMER_KIND, t.kind)}：${fmtTime(t.due_at)}`)))
        : '没有'],
    ]),
    h('p', { class: 'hint' }, '总开关、档位和免打扰时段在',
      h('a', { href: href('conversations', key, 'policy') }, '「策略」'), '里修改。'));
}

// autonomyTab shows a private chat's hidden state: its status, bars, recent
// bar changes and how the bot starts the chat. write is conversation.js's
// write, which reloads the tab after a change.
export async function autonomyTab({ key, conv, signal, write }) {
  const [state, policy] = await Promise.all([
    get(convPath(key, 'autonomy'), null, signal),
    get(convPath(key, 'policy'), null, signal),
  ]);
  const priv = ((((policy.current || {}).policy || {}).proactive || {}).private) || {};
  return [
    statusSection(key, state.status, write),
    barsSection(key, state.bars || [], write),
    eventsSection(state.events || []),
    proactiveSection(key, conv, state.proactive || {}, priv),
  ];
}
