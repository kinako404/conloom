import { h, mount, errorBox, loading, empty } from '../lib/dom.js';
import { get, convPath, currentUser, isAbort } from '../lib/api.js';
import { href } from '../lib/router.js';
import { fmtTime, fmtInt, label, sceneLabel } from '../lib/format.js';
import { avatarEl } from './avatar.js';
import { listen } from './chat.js';
import { personaLabel } from './conversations.js';
import { EVENT_KIND, eventChange } from './autonomy.js';

// POLL_MS is how often the card refreshes the hidden state while open.
export const POLL_MS = 5000;
// AVATAR is how many pixels across the card's avatar is.
const AVATAR = 96;
// LONG is how many characters a text has before the card collapses it, and
// PREVIEW how many of them show while it is collapsed.
const LONG = 80;
const PREVIEW = 40;
// PANEL is the platform of conversations held in the panel (store.PlatformPanel).
const PANEL = 'panel';

const GENDER = { female: '女', male: '男' };

// panelActorID is the user ID of the panel user user in panel chats, as
// core.PanelActorID.
function panelActorID(user) {
  return 'panel_' + String(user || '').replace(/:/g, '_');
}

// minutesLeft is how many minutes are left until the RFC 3339 time until,
// rounded up, at least 1.
function minutesLeft(until) {
  return Math.max(1, Math.ceil((new Date(until).getTime() - Date.now()) / 60000));
}

// statusLine words what the bot is doing in the chat of state: its activity
// with the time left while a status lasts, otherwise the scene when the bot
// enables it, otherwise ''.
function statusLine(state) {
  if (!state) return '';
  if (state.status) return `正在：${state.status.activity}（还剩 ${minutesLeft(state.status.until_at)} 分钟）`;
  const scene = state.scene || {};
  return scene.enabled ? sceneLabel(scene) : '';
}

// item is a row of a section: value on top, its muted label under it.
function item(value, text) {
  return h('div', null, h('div', { class: 'tg-profile-row' }, value), h('div', { class: 'tg-profile-label' }, text));
}

// longText shows text, collapsed to its first PREVIEW characters with a
// toggle when it is longer than LONG characters, or than PREVIEW when always
// is set.
function longText(text, always = false) {
  const chars = [...text];
  if (chars.length <= (always ? PREVIEW : LONG)) return text;
  const preview = chars.slice(0, PREVIEW).join('') + '…';
  const summary = h('summary', null, preview);
  const details = h('details', null, summary, h('div', { class: 'pre' }, text));
  details.addEventListener('toggle', () => {
    summary.textContent = details.open ? '收起' : preview;
  });
  return details;
}

function section(title, body) {
  return h('section', { class: 'tg-profile-section' }, h('h3', null, title), body);
}

// stateBody shows the bars and running effects of state, or that the bot has
// none without the buff skill. skills is null when unknown; then a bot with
// bars is taken to use them.
function stateBody(state, skills) {
  const bars = state.bars || [];
  const effects = state.effects || [];
  const buff = skills ? skills.includes('buff') : bars.length > 0;
  if (!buff) return h('p', { class: 'hint' }, '这个 bot 没有启用 buff 技能，没有状态条。');
  if (!bars.length && !effects.length) return empty('没有状态条');
  return [
    bars.map((b) => {
      const fill = h('span', { class: 'fill' });
      fill.style.width = `${Math.min(Math.max(Number(b.value) || 0, 0), 100)}%`;
      return item([
        h('div', { class: 'tg-profile-bar', role: 'img', 'aria-label': `${b.name} ${b.value}` }, fill),
        `${b.value} · ${b.band}`,
      ], b.name);
    }),
    effects.map((e) => item(e.name, `还剩 ${minutesLeft(e.until_at)} 分钟`)),
  ];
}

// person is the member of memory m the chat conv is with: the panel user in
// a panel chat, the peer of another private chat, else the only member.
function person(conv, m) {
  const members = m.members || [];
  const id = conv.platform === PANEL ? panelActorID(currentUser()) : conv.platform_conversation_id;
  return members.find((x) => x.user_id === id) || (members.length === 1 ? members[0] : null);
}

function relationBody(conv, m) {
  if (!m.enabled) return h('p', { class: 'hint' }, '这个聊天没有开启记忆。');
  const who = person(conv, m);
  const affinity = who && who.affinity !== null && who.affinity !== undefined
    ? `${who.affinity}${who.tier ? `（${who.tier}）` : ''}` : '还没有';
  return [
    item(affinity, who && who.name ? `${who.name} 的好感度` : '好感度'),
    item(who && who.note ? longText(who.note) : '还没有备注', '备注'),
    item(m.summary ? longText(m.summary, true) : '还没有摘要', '摘要'),
  ];
}

function profileBody(conv, res) {
  const doc = res.current ? res.current.persona : res.template_doc;
  const p = doc || {};
  return [
    p.character_name ? item(p.character_name, '角色') : null,
    p.description ? item(longText(p.description), '设定') : null,
    p.gender ? item(label(GENDER, p.gender), '性别') : null,
    conv ? item(personaLabel(conv), '人设') : null,
  ];
}

function listRow(icon, name, count) {
  return [
    h('span', { class: 'tg-profile-icon', 'aria-hidden': 'true' }, icon),
    h('span', null, name),
    h('span', { class: 'tg-profile-count' }, count),
  ];
}

function eventsRow(events) {
  return h('li', null, h('details', null,
    h('summary', null, listRow('↻', '最近变化', fmtInt(events.length))),
    events.length
      ? h('ul', { class: 'plain' }, events.map((e) => h('li', null,
        h('div', null, `${e.name} · ${label(EVENT_KIND, e.kind)} · ${eventChange(e)}`),
        h('div', { class: 'tg-profile-label' }, [fmtTime(e.at), e.reason].filter(Boolean).join(' · ')))))
      : empty('最近没有变化')));
}

// openProfile opens the profile card of the chat key with bot ({id, name,
// avatar, skills when known}) over the page: its persona, and in a private
// chat its hidden state and how the bot relates to the person, each section
// from its own request so one that fails shows its error there only. While
// open it refreshes the hidden state every POLL_MS. ×, Escape, a click on
// the backdrop or signal, the page's, close it; close() ends its requests,
// timer and listeners and returns the focus to opener.
export function openProfile({ key, bot, signal, opener }) {
  if (signal && signal.aborted) return { close() {} };
  const ctrl = new AbortController();
  const who = bot || { id: key, name: key };
  let timer = 0;
  let closed = false;

  const close = () => {
    if (closed) return;
    closed = true;
    clearTimeout(timer);
    ctrl.abort();
    backdrop.remove();
    if (opener) opener.focus();
  };

  const avatar = h('div', { class: 'tg-profile-avatar' }, avatarEl(who, AVATAR));
  const name = h('div', { class: 'tg-profile-name', id: 'tg-profile-name' }, who.name || who.id);
  const status = h('div', { class: 'tg-profile-status' });
  const edit = h('a', {
    class: 'tg-profile-edit', href: href('conversations', key), title: '在会话详情里编辑', 'aria-label': '在会话详情里编辑', onclick: close,
  }, '✎');
  const shut = h('button', { type: 'button', class: 'tg-profile-close', title: '关闭', 'aria-label': '关闭', onclick: close }, '×');
  const stateEl = h('div', null, loading());
  const relationEl = h('div', null, loading());
  const profileEl = h('div', null, loading());
  const listEl = h('ul', { class: 'tg-profile-list' });
  const privateEls = h('div');
  const card = h('section', { class: 'tg-profile', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': 'tg-profile-name', tabindex: '-1' },
    h('div', { class: 'tg-profile-head' },
      h('div', { class: 'tg-profile-actions' }, edit, shut),
      avatar, name, status),
    privateEls,
    section('资料', profileEl),
    listEl);
  const backdrop = h('div', { class: 'tg-profile-backdrop' }, card);

  backdrop.addEventListener('click', (e) => {
    if (e.target === backdrop) close();
  });
  card.addEventListener('keydown', (e) => {
    if (e.key !== 'Tab') return;
    const stops = [...card.querySelectorAll('a[href], button, summary')].filter((el) => !el.disabled && el.offsetParent !== null);
    if (!stops.length) return;
    const first = stops[0];
    const last = stops[stops.length - 1];
    if (e.shiftKey && (document.activeElement === first || document.activeElement === card)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  });
  listen(document, 'keydown', (e) => {
    if (e.key === 'Escape') close();
  }, ctrl.signal);
  if (signal) listen(signal, 'abort', close, ctrl.signal);
  document.body.appendChild(backdrop);
  card.focus();

  const isPrivate = (c) => c.type === 'private';
  const convP = get(convPath(key), null, ctrl.signal);
  const personaP = get(convPath(key, 'persona'), null, ctrl.signal);
  const memoryP = convP.then((c) => (isPrivate(c) ? get(convPath(key, 'memory'), null, ctrl.signal) : null));
  const autonomyP = convP.then((c) => (isPrivate(c) ? get(convPath(key, 'autonomy'), null, ctrl.signal) : null));
  const skillsP = Array.isArray(who.skills)
    ? Promise.resolve(who.skills)
    : get('bots', null, ctrl.signal).then((res) => {
      const b = (res.bots || []).find((x) => x.id === who.id);
      return b ? b.skills || [] : null;
    });

  let skills = null;
  const showState = (res) => {
    if (res.status === 'rejected') {
      if (!isAbort(res.reason)) mount(stateEl, errorBox(res.reason));
      return;
    }
    mount(status, statusLine(res.value));
    mount(stateEl, stateBody(res.value, skills));
  };
  const poll = async () => {
    let res;
    try {
      res = { status: 'fulfilled', value: await get(convPath(key, 'autonomy'), null, ctrl.signal) };
    } catch (err) {
      res = { status: 'rejected', reason: err };
    }
    if (closed) return;
    showState(res);
    timer = setTimeout(poll, POLL_MS);
  };

  Promise.allSettled([convP, personaP, memoryP, autonomyP, skillsP]).then(([convR, personaR, memoryR, autonomyR, skillsR]) => {
    if (closed) return;
    const conv = convR.status === 'fulfilled' ? convR.value : null;
    skills = skillsR.status === 'fulfilled' ? skillsR.value : null;

    if (personaR.status === 'rejected') {
      mount(profileEl, errorBox(personaR.reason), conv ? item(personaLabel(conv), '人设') : null);
    } else {
      const doc = (personaR.value.current ? personaR.value.current.persona : personaR.value.template_doc) || {};
      if (doc.character_name) mount(name, doc.character_name);
      if (doc.avatar_image && doc.avatar_image !== who.avatar) mount(avatar, avatarEl({ ...who, avatar: doc.avatar_image }, AVATAR));
      mount(profileEl, profileBody(conv, personaR.value));
    }

    const rows = [];
    if (conv && isPrivate(conv)) {
      mount(privateEls, section('状态', stateEl), section('关系', relationEl));
      showState(autonomyR);
      if (memoryR.status === 'rejected') mount(relationEl, errorBox(memoryR.reason));
      else mount(relationEl, relationBody(conv, memoryR.value));
      if (autonomyR.status !== 'fulfilled') rows.push(h('li', null, listRow('↻', '最近变化', '—')));
      else rows.push(eventsRow(autonomyR.value.events || []));
      timer = setTimeout(poll, POLL_MS);
    }
    rows.push(h('li', null, listRow('✉', '消息', conv ? fmtInt(conv.message_count) : '—')));
    rows.push(h('li', null, listRow('★', '技能', skills ? (skills.length ? skills.join('、') : '没有') : '—')));
    mount(listEl, convR.status === 'rejected' ? h('li', null, errorBox(convR.reason)) : null, rows);
  });

  return { close };
}
