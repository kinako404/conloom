import { h, mount, errorBox, empty, table, kv } from '../lib/dom.js';
import { get, put, del, convPath } from '../lib/api.js';
import { href } from '../lib/router.js';
import { fmtTime, label, sceneLabel, PRIVATE_LEVEL, SCENE_PLACE_MAX } from '../lib/format.js';

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

// EVENT_KIND are the kinds of a change of a bar or an effect.
const EVENT_KIND = {
  adjust: '调整',
  bar_add: '新增状态条',
  bar_remove: '删除状态条',
  effect_add: '新增效果',
  effect_remove: '去掉效果',
};

// PANEL is the platform of conversations held in the panel (store.PlatformPanel).
const PANEL = 'panel';

// sceneSection shows whether the bot and the person chat online or are
// together in person, and where, and switches it.
function sceneSection(key, scene, write) {
  const title = h('h2', null, '场景');
  if (!scene.enabled) {
    return h('section', { class: 'card' }, title, h('p', { class: 'hint' }, 'bot 没有启用 scene 技能，不区分线上和面对面。'));
  }
  const msg = h('div');
  const path = convPath(key, 'autonomy', 'scene');
  const online = h('button', {
    type: 'button',
    onclick: () => write(msg, online, () => put(path, { mode: 'online', place: '' }), '已切到线上'),
  }, '切到线上');
  const placeInput = h('input', { placeholder: '地点（可选，最多 16 字）', 'aria-label': '面对面的地点' });
  const inPerson = h('button', { type: 'submit' }, '切到面对面');
  const form = h('form', {
    class: 'toolbar',
    onsubmit: (e) => {
      e.preventDefault();
      const place = placeInput.value.trim();
      if ([...place].length > SCENE_PLACE_MAX) {
        mount(msg, errorBox({ message: '地点最多 16 个字' }));
        return undefined;
      }
      return write(msg, inPerson, () => put(path, { mode: 'in_person', place }), '已切到面对面');
    },
  }, placeInput, inPerson);
  return h('section', { class: 'card' },
    title,
    kv([['现在', sceneLabel(scene)], ['开始', fmtTime(scene.since_at)]]),
    scene.idle_online ? h('p', { class: 'hint warn' }, '超过 3 小时没说话，已回到线上') : null,
    h('p', { class: 'hint' }, '模型用 set_scene 切换；面对面时对方超过 3 小时没说话，会自动回到线上。'),
    h('div', { class: 'toolbar' }, online),
    form,
    msg);
}

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

// removeButton deletes, once ask is confirmed, a bar or an effect the bot
// added at path.
function removeButton(msg, write, ask, path, done) {
  const button = h('button', {
    type: 'button',
    class: 'danger',
    onclick: () => {
      if (!confirm(ask)) return undefined;
      return write(msg, button, () => del(path), done);
    },
  }, '删除');
  return button;
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

// barPath is the API path of the bar name of the chat key.
function barPath(key, name) {
  return convPath(key, 'autonomy', 'bars', encodeURIComponent(name));
}

// shadowedNote tells of a bar the bot added that a persona bar of the same
// name has replaced, and offers to delete it.
function shadowedNote(key, name, msg, write) {
  return h('p', { class: 'hint warn' },
    `bot 自建的「${name}」和人设里的状态条同名，已由人设的那条代替、不再生效，但仍占一个自建名额。`, ' ',
    removeButton(msg, write, `删除 bot 自建的状态条「${name}」？人设里的「${name}」和它现在的数值保留。`,
      barPath(key, name), `已删除 bot 自建的「${name}」`));
}

function barsSection(key, bars, shadowed, write) {
  const msg = h('div');
  const columns = [
    {
      title: '状态条',
      cell: (b) => [
        h('div', null, b.name, b.custom ? [' ', h('span', { class: 'badge' }, '自建')] : null),
        b.description ? h('div', { class: 'muted small' }, b.description) : null,
      ],
    },
    {
      title: '当前',
      class: 'nowrap',
      cell: (b) => [h('meter', { min: 0, max: 100, value: b.value, 'aria-label': b.name }), ` ${b.value}（${b.band}）`],
    },
    { title: '基准', class: 'num', cell: (b) => String(b.baseline) },
    { title: '设为', cell: (b) => barEditor(key, b, msg, write) },
  ];
  if (bars.some((b) => b.custom)) {
    columns.push({
      title: '操作',
      cell: (b) => (b.custom
        ? removeButton(msg, write, `删除 bot 自建的状态条「${b.name}」？它的数值和说明会一起删除。`,
          barPath(key, b.name), `已删除状态条「${b.name}」`)
        : null),
    });
  }
  return h('section', { class: 'card' },
    h('h2', null, '状态条'),
    h('p', { class: 'hint' }, '数值 0–100，按各自的半衰期逐渐回到基准。在这里设的值立即生效，不受模型调整的单次和每日上限约束。只有 bot 启用了 buff 技能时，状态条才会影响回复。'),
    h('p', { class: 'hint' }, '先列人设里的，再列标“自建”的：bot 用 add_buff 自己加的，可以删除；人设里的不能删除。面板不能新增状态条。'),
    bars.length ? table(columns, bars) : empty('没有状态条'),
    shadowed.map((name) => shadowedNote(key, name, msg, write)),
    msg);
}

// timeLeft words how long an effect has left until an RFC 3339 time, rounded
// up to the minute below a day and to the hour from a day on, as the bot's
// state does: "约 1 小时 20 分", "约 2 天 3 小时".
function timeLeft(until) {
  const minutes = Math.ceil((new Date(until).getTime() - Date.now()) / 60000);
  if (!(minutes > 0)) return '已到期';
  const parts = [];
  const part = (n, unit) => { if (n > 0) parts.push(`${n} ${unit}`); };
  if (minutes >= 24 * 60) {
    const hours = Math.ceil(minutes / 60);
    part(Math.floor(hours / 24), '天');
    part(hours % 24, '小时');
  } else {
    part(Math.floor(minutes / 60), '小时');
    part(minutes % 60, '分');
  }
  return `约 ${parts.join(' ')}`;
}

function effectsSection(key, effects, write) {
  const msg = h('div');
  return h('section', { class: 'card' },
    h('h2', null, '效果'),
    h('p', { class: 'hint' }, 'bot 用 add_buff 给自己加的临时效果，到时间自动消失，也可以在这里提前删除。面板不能新增效果。只有 bot 启用了 buff 技能时，效果才会影响回复。'),
    effects.length ? table([
      {
        title: '效果',
        cell: (e) => [h('div', null, e.name), e.description ? h('div', { class: 'muted small' }, e.description) : null],
      },
      { title: '剩余', class: 'nowrap', cell: (e) => timeLeft(e.until_at) },
      { title: '开始', class: 'nowrap', cell: (e) => fmtTime(e.started_at) },
      { title: '结束', class: 'nowrap', cell: (e) => fmtTime(e.until_at) },
      {
        title: '操作',
        cell: (e) => removeButton(msg, write, `提前去掉效果「${e.name}」？`,
          convPath(key, 'autonomy', 'effects', encodeURIComponent(e.name)), `已去掉效果「${e.name}」`),
      },
    ], effects) : empty('现在没有效果'),
    msg);
}

// eventChange shows what a change did: the values of an adjustment, the
// starting value of an added bar.
function eventChange(e) {
  if (e.kind === 'adjust') return `${e.before} → ${e.after}`;
  if (e.kind === 'bar_add') return `初始 ${e.after}`;
  return '—';
}

function eventsSection(events) {
  return h('section', { class: 'card' },
    h('h2', null, '最近变化'),
    h('p', { class: 'hint' }, '最近 50 次状态条和效果的变化（调整数值、新增和删除），新的在前，保留 30 天。'),
    events.length ? table([
      { title: '时间', class: 'nowrap', cell: (e) => fmtTime(e.at) },
      { title: '名称', cell: (e) => e.name },
      { title: '类型', class: 'nowrap', cell: (e) => label(EVENT_KIND, e.kind) },
      { title: '变化', class: 'nowrap', cell: eventChange },
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

// autonomyTab shows a private chat's hidden state: its scene, status, bars,
// effects, their recent changes and how the bot starts the chat. write is
// conversation.js's write, which reloads the tab after a change.
export async function autonomyTab({ key, conv, signal, write }) {
  const [state, policy] = await Promise.all([
    get(convPath(key, 'autonomy'), null, signal),
    get(convPath(key, 'policy'), null, signal),
  ]);
  const priv = ((((policy.current || {}).policy || {}).proactive || {}).private) || {};
  return [
    sceneSection(key, state.scene || {}, write),
    statusSection(key, state.status, write),
    barsSection(key, state.bars || [], state.shadowed_bars || [], write),
    effectsSection(key, state.effects || [], write),
    eventsSection(state.events || []),
    proactiveSection(key, conv, state.proactive || {}, priv),
  ];
}
