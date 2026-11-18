import { h, mount, errorBox } from '../lib/dom.js';
import { get, post, del, convPath, isAbort } from '../lib/api.js';
import { go, href } from '../lib/router.js';
import { fmtTime, sceneLabel, actionParts } from '../lib/format.js';
import { avatarEl } from './avatar.js';
import { commandMenu } from './commandmenu.js';
import { openProfile } from './profile.js';

// POLL_MS is how often the chat asks for new messages.
export const POLL_MS = 1500;
// HISTORY is how many recent messages the chat opens with.
export const HISTORY = 50;
// PAGE is the most messages one GET chat returns (maxChatMessages); a full
// page is followed by another poll right away.
export const PAGE = 200;
// TOUCH matches a device whose main pointer is a finger. Its on-screen
// keyboard has no Shift+Return, so Return types a newline and the send
// button (➤ here, 发送 on the classic chat page) sends.
export const TOUCH = '(pointer: coarse)';
// SUGGEST_KEY is where the browser keeps the 建议回复 switch, which is on
// unless set to SUGGEST_OFF.
const SUGGEST_KEY = 'conloom.chat.suggestions';
const SUGGEST_OFF = 'off';
// MAX_LINES is how many lines the message box grows to before it scrolls.
const MAX_LINES = 6;
// LONG_PRESS_MS is how long a finger rests on a message to open its menu;
// moving it more than LONG_PRESS_SLOP pixels meanwhile makes it a scroll.
export const LONG_PRESS_MS = 450;
const LONG_PRESS_SLOP = 10;
// FOLLOW_MS is how long after the message menu opens by a long press or the
// keyboard, or after the finger that opened it lifts, the browser's own click
// or contextmenu that follows is ignored.
const FOLLOW_MS = 500;
// MENU_MARGIN keeps the message menu this many pixels inside the window.
const MENU_MARGIN = 4;
// TOAST_MS is how long a note such as 已复制 shows.
const TOAST_MS = 1500;
// CLICK_FOLLOW_MS is how long after a right-click opens the message menu a
// click is taken for the one some platforms send with it (macOS Ctrl+click)
// and does not close the menu.
const CLICK_FOLLOW_MS = 300;
// BUBBLE_KEYS move the focus between bubbles.
const BUBBLE_KEYS = new Set(['ArrowUp', 'ArrowDown', 'Home', 'End']);
// SCROLL_KEYS are the keys that scroll the page or the messages.
const SCROLL_KEYS = new Set(['PageUp', 'PageDown', 'Home', 'End', 'ArrowUp', 'ArrowDown', ' ']);

// suggestionsOn reads the 建议回复 switch; a browser that refuses storage
// gets it on.
export function suggestionsOn() {
  try {
    return localStorage.getItem(SUGGEST_KEY) !== SUGGEST_OFF;
  } catch {
    return true;
  }
}

export function saveSuggestionsOn(on) {
  try {
    if (on) localStorage.removeItem(SUGGEST_KEY);
    else localStorage.setItem(SUGGEST_KEY, SUGGEST_OFF);
  } catch {
    // Without storage the switch lasts as long as the page.
  }
}

const pad = (n) => String(n).padStart(2, '0');

// dayOf is the local day of the RFC 3339 time v, '' when v is not a time.
function dayOf(v) {
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? '' : d.toDateString();
}

// dayLabel names the local day of the time v as Telegram does: 11月17日 in
// the year of now, 2025年11月17日 in another.
function dayLabel(v, now = new Date()) {
  const d = new Date(v);
  const md = `${d.getMonth() + 1}月${d.getDate()}日`;
  return d.getFullYear() === now.getFullYear() ? md : `${d.getFullYear()}年${md}`;
}

// clock is the local time of v as HH:MM, '' when v is not a time.
function clock(v) {
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? '' : `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// bubble shows message m: the bot's on the left, the user's on the right,
// with its time in the corner, its sender and full time on hover, and its
// channel only when it is not the panel; an action in full-width brackets,
// such as （笑）, is muted (see actionParts). It holds no buttons: its
// actions are in the message menu (see chatPane), which the keyboard opens on
// the bubble once it has the focus. It is out of the Tab order until chatPane
// makes it the Tab stop.
function bubble(m) {
  const when = fmtTime(m.at);
  return h('li', { class: m.from_bot ? 'tg-msg in' : 'tg-msg out', dataset: { seq: String(m.seq) } },
    h('div', { class: 'tg-bubble', tabindex: '-1', 'aria-haspopup': 'menu' },
      m.channel && m.channel !== 'panel' ? h('div', null, h('span', { class: 'badge' }, m.channel)) : null,
      h('span', { class: 'tg-text' }, actionParts(m.text).map((p) => (p.action ? h('span', { class: 'tg-action' }, p.text) : p.text))),
      h('span', { class: 'tg-time', title: m.sender_name ? `${m.sender_name} · ${when}` : when }, clock(m.at))));
}

// listen adds fn as a listener of type on target, in the capture phase when
// capture is true, until signal fires.
export function listen(target, type, fn, signal, capture = false) {
  target.addEventListener(type, fn, capture);
  signal.addEventListener('abort', () => target.removeEventListener(type, fn, capture));
}

// shownMenuClose closes the context menu open on the page, if any. Only one
// is open at a time: menuOpened(close), called by a menu that opens with its
// close, closes any other first, and menuClosed(close) tells that it closed.
let shownMenuClose = null;

export function menuOpened(close) {
  if (shownMenuClose && shownMenuClose !== close) {
    const other = shownMenuClose;
    shownMenuClose = null;
    other();
  }
  shownMenuClose = close;
}

export function menuClosed(close) {
  if (shownMenuClose === close) shownMenuClose = null;
}

// copyText puts text on the clipboard and reports whether it did. Without the
// Clipboard API (a page not served over HTTPS, an old browser) it copies from
// a hidden box.
export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    // falls back to the hidden box
  }
  const box = h('textarea', { class: 'tg-copy-buffer', readonly: true, tabindex: '-1', 'aria-hidden': 'true' });
  box.value = text;
  document.body.appendChild(box);
  box.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  box.remove();
  return ok;
}

// GONE is shown, once, when the chat no longer exists.
export const GONE = '这个聊天已不存在，可能已经被删除。';

// MESSAGE_GONE is the error code of a withdraw or regenerate of a message
// that no longer exists in a chat that does, and MESSAGE_GONE_NOTE what is
// shown when the chat is opened afresh after one.
export const MESSAGE_GONE = 'MESSAGE_NOT_FOUND';
export const MESSAGE_GONE_NOTE = '这条消息已经不在了（可能在别的窗口里撤回了），聊天已重新载入。';

// ACTION_BUSY is shown when a withdraw or regenerate is refused because the
// bot is still replying.
export const ACTION_BUSY = '对方正在回复，等它说完再试。';

// SUPPRESSED says why a sent message, stored all the same, gets no reply, by
// the suppressed_reason of the 202 to POST messages.
const SUPPRESSED = {
  paused: '这个聊天暂停中，bot 不会回复这条消息。',
  rate_limited: '发得太快了，bot 不会回复这条消息，稍后再发。',
  queue_full: '待回复的消息太多，bot 不会回复这条消息，稍后再发。',
  stale: '这条消息到得太晚，bot 不会回复。',
};

export function suppressedNote(reason) {
  const text = Object.prototype.hasOwnProperty.call(SUPPRESSED, reason)
    ? SUPPRESSED[reason] : `bot 不会回复这条消息（${reason}）。`;
  return h('p', { class: 'hint warn', role: 'status' }, text);
}

// deleteChatConfirmed deletes the panel chat key once the user confirms, and
// reports whether it did; a failed delete throws.
export async function deleteChatConfirmed(key) {
  if (!confirm('删除这个聊天？聊天记录、人设和策略都会删除，不能恢复；在这个聊天里设的状态会结束。这个 bot 所有面板聊天共用的记忆保留，用量统计和审计记录也保留。')) return false;
  await del(convPath(key));
  return true;
}

// chatBar tops the pane of the chat key like Telegram: ← back to the list
// (onBack), shown on a narrow screen; the avatar and name of bot, null when
// the chat is not known, with title, who the bot talks with, on hover and
// status, when given, under the name, which together, when the bar has key and
// bot, are one button, 查看资料, that opens the chat's profile card (see
// openProfile; signal closes it too); and a ⋮ menu with 会话详情, items and,
// when onDelete is given, 删除这个聊天. Choosing an item, a click elsewhere or
// Escape anywhere on the page closes the menu; signal ends the listening for
// those.
export function chatBar({ key, bot, title, status, items, onBack, onDelete, signal }) {
  const more = h('button', {
    type: 'button', class: 'tg-bar-more', title: '更多', 'aria-label': '更多', 'aria-haspopup': 'menu', 'aria-expanded': 'false',
  }, '⋮');
  const menu = h('div', { class: 'tg-bar-menu', role: 'menu', hidden: true },
    h('a', { class: 'tg-bar-item', role: 'menuitem', href: href('conversations', key) }, '会话详情'),
    items || null,
    onDelete ? h('button', { type: 'button', class: 'tg-bar-item danger', role: 'menuitem', onclick: onDelete }, '删除这个聊天') : null);
  const show = (open) => {
    menu.hidden = !open;
    more.setAttribute('aria-expanded', String(open));
  };
  more.addEventListener('click', () => show(menu.hidden));
  menu.addEventListener('click', (e) => {
    if (e.target.closest('.tg-bar-item')) show(false);
  });
  listen(document, 'click', (e) => {
    if (!menu.hidden && !menu.contains(e.target) && !more.contains(e.target)) show(false);
  }, signal);
  listen(document, 'keydown', (e) => {
    if (e.key === 'Escape' && !menu.hidden) {
      show(false);
      more.focus();
    }
  }, signal);
  const who = [
    bot ? avatarEl(bot, 40) : null,
    h('span', { class: 'tg-bar-name', title: title || null },
      h('strong', null, bot ? bot.name || bot.id : key),
      status || h('span', { class: 'tg-bar-status' })),
  ];
  const profile = key && bot ? h('button', {
    type: 'button', class: 'tg-bar-profile', title: '查看资料', 'aria-label': '查看资料',
    onclick: () => openProfile({ key, bot, signal, opener: profile }),
  }, who) : null;
  return h('header', { class: 'tg-bar' },
    h('button', { type: 'button', class: 'tg-bar-back', title: '返回', 'aria-label': '返回', onclick: onBack }, '←'),
    profile || who,
    h('div', { class: 'tg-bar-actions' }, more, menu));
}

// chatPane talks to the bot in the panel chat key, as the panel user, and
// returns its top bar (see chatBar) followed by the conversation. It opens
// with the latest messages, then polls GET chat with the newest seq it has
// seen until signal fires or the chat is gone (404), which stops the polling
// and the sending. Messages are bubbles grouped by sender, with a date pill
// where the day changes; those that arrive after the opening ones float in,
// and 正在输入 shows under the name while the bot is busy, otherwise, while
// the chat is in person, the scene, such as 面对面 · 食堂. With the 建议回复
// switch on, once the bot's turn is done it offers reply suggestions under
// the newest message, which send themselves when clicked. hint, when given,
// is shown above the messages. bot ({id, name, avatar}) is who the chat is
// with, title who the bot talks with; onBack() returns to the chat list and
// onDelete() deletes the chat after confirmation. onMessages(messages), when
// given, gets the messages each time some are shown: the latest at first,
// then each one new. Typing the command prefix in the box opens the command
// menu (see commandMenu).
// A right-click, a long press or the menu key on a message opens its menu,
// where it can be copied (复制), withdrawn (撤回, with what follows it) and,
// the one GET chat names in regenerate_seq, regenerated (重新生成); the last
// two are off while the bot is busy or a message is being sent, and only one
// menu of the page is open at a time (see menuOpened). Polling stops before
// a withdraw or regenerate is asked for and a poll already under way is
// dropped, so no message they delete is shown again; once done, reload(note)
// opens the chat afresh (by default the route of key), with note, when given,
// shown there: a message that is gone already reloads with MESSAGE_GONE_NOTE,
// a chat that is gone stops the pane as a 404 of the polling does.
export async function chatPane({ key, signal, hint, bot, title, onBack, onDelete, onMessages, reload = () => go(href('im', key)) }) {
  const log = h('ul', { class: 'tg-log' }, hint ? h('li', { class: 'tg-note' }, hint) : null);
  // typing is 正在输入 with three blinking dots, always in the status line,
  // which keeps its height, and shown by showTyping, which fades it. sceneEl,
  // stacked under it, names the scene GET chat gives while the chat is in
  // person and the bot is not typing, which takes precedence (see showScene).
  const typing = h('span', { class: 'tg-typing', 'aria-hidden': 'true' }, '正在输入',
    h('span', { class: 'tg-typing-dot' }), h('span', { class: 'tg-typing-dot' }), h('span', { class: 'tg-typing-dot' }));
  const sceneEl = h('span', { class: 'tg-scene', hidden: true });
  const status = h('span', { class: 'tg-bar-status' }, sceneEl, typing);
  const showTyping = (on) => {
    typing.classList.toggle('on', on);
    typing.setAttribute('aria-hidden', String(!on));
  };
  const showScene = (scene) => {
    sceneEl.textContent = sceneLabel(scene);
    sceneEl.hidden = !(scene && scene.enabled && scene.mode === 'in_person') || botTyping;
  };
  const pollError = h('div', { class: 'tg-status' });
  const sendError = h('div', { class: 'tg-status' });
  const touch = matchMedia(TOUCH).matches;
  const input = h('textarea', {
    class: 'tg-input', rows: 1, placeholder: touch ? '输入消息' : '输入消息，回车发送，Shift+回车换行',
  });
  const send = h('button', { type: 'submit', class: 'tg-send', title: '发送', 'aria-label': '发送' }, '➤');
  let suggestOn = suggestionsOn();
  const suggestItem = h('button', { type: 'button', class: 'tg-bar-item', role: 'menuitem' });
  const showSwitch = () => {
    suggestItem.textContent = suggestOn ? '建议回复：开' : '建议回复：关';
  };
  showSwitch();
  // chips holds the suggestions; it is in the log, after the newest message,
  // only while it shows some.
  const chips = h('li', { class: 'tg-keys' });
  let after = 0;
  // last is the newest message shown and lastEl its bubble, lastDay the
  // newest day with a date pill; botTyping and regenSeq are what the last
  // poll said, asked the seq suggestions were last asked for and sentAfter
  // the seq of the newest message when the user last sent one. acting is set
  // while a withdraw or regenerate is under way, and epoch counts those
  // started: a poll that began before one is dropped.
  let last = null;
  let lastEl = null;
  let lastDay = '';
  let botTyping = true;
  let regenSeq = 0;
  let asked = 0;
  let sentAfter = 0;
  let gone = false;
  let acting = false;
  let epoch = 0;

  const nearBottom = () => log.scrollHeight - log.scrollTop - log.clientHeight < 40;
  const clearSuggestions = () => {
    mount(chips);
    chips.remove();
  };

  // The message menu floats over the page, outside the scrolling messages:
  // 复制, 重新生成 for the message regenSeq names, and 撤回, of the message
  // menuMsg, whose bubble, menuBubble, is marked tg-selected while it is
  // open. menuVia tells what opened it, at menuAt: 'mouse' (a right-click),
  // 'touch' (a long press, which leaves the focus where it was, so an
  // on-screen keyboard stays) or 'key' (the menu key or Shift+F10), and
  // menuBack where the focus goes back when it closes (see closeMenu). press
  // is the finger resting on a bubble, until it lifts, moves or the list
  // scrolls; clicks before followUntil, those a long press ends with or a
  // right-click may bring, do not close the menu.
  const messages = new Map();
  const copyItem = h('button', { type: 'button', class: 'tg-bar-item', role: 'menuitem', onclick: () => choose((m) => copyMessage(m)) }, '复制');
  const regenItem = h('button', { type: 'button', class: 'tg-bar-item', role: 'menuitem', onclick: () => choose((m) => actions.regenerate(m)) }, '重新生成');
  const withdrawItem = h('button', { type: 'button', class: 'tg-bar-item danger', role: 'menuitem', onclick: () => choose((m) => actions.withdraw(m)) }, '撤回');
  const msgMenu = h('div', { class: 'tg-ctx-menu tg-msg-menu', role: 'menu' }, copyItem, regenItem, withdrawItem);
  let menuBubble = null;
  let menuMsg = null;
  let menuVia = '';
  let menuAt = 0;
  let menuBack = null;
  let press = null;
  let followUntil = 0;
  let toastEl = null;
  // Of the bubbles, only tabStop is in the Tab order (roving tabindex): the
  // one last focused, chosen, or else the newest.
  let tabStop = null;
  let chosenStop = false;
  const setTabStop = (b) => {
    if (tabStop === b) return;
    if (tabStop) tabStop.tabIndex = -1;
    tabStop = b;
    b.tabIndex = 0;
  };

  // refreshActions shows, in the open message menu, 重新生成 only on the
  // message regenSeq names, and turns 重新生成 and 撤回 off while they cannot
  // run; the focus on an item turned off moves to 复制.
  const refreshActions = () => {
    if (!menuBubble) return;
    const off = botTyping || gone || acting || send.disabled;
    const focused = msgMenu.contains(document.activeElement);
    regenItem.hidden = !regenSeq || menuMsg.seq !== regenSeq;
    regenItem.disabled = off;
    withdrawItem.disabled = off;
    if (focused && !msgMenu.contains(document.activeElement)) copyItem.focus({ preventScroll: true });
  };
  // placeMenu puts the menu's top left at x, y, its top right when fromRight,
  // or, when it does not fit under y, its bottom at above; it stays inside the
  // window and grows from where it was opened.
  const placeMenu = (x, y, above, fromRight) => {
    const w = msgMenu.offsetWidth;
    const ht = msgMenu.offsetHeight;
    const maxLeft = window.innerWidth - w - MENU_MARGIN;
    const maxTop = window.innerHeight - ht - MENU_MARGIN;
    const left = Math.max(MENU_MARGIN, Math.min(fromRight ? x - w : x, maxLeft));
    const top = Math.max(MENU_MARGIN, Math.min(y <= maxTop ? y : above - ht, maxTop));
    msgMenu.style.left = `${left}px`;
    msgMenu.style.top = `${top}px`;
    msgMenu.style.transformOrigin = `${Math.min(Math.max(x - left, 0), w)}px ${Math.min(Math.max(y - top, 0), ht)}px`;
  };
  // closeMenu closes the message menu. When the focus was in it, or nowhere,
  // it goes back to menuBack: the bubble when the keyboard opened the menu,
  // else what had it before, such as the message box; a click that put it
  // somewhere else leaves it there.
  const closeMenu = () => {
    if (!menuBubble) return;
    const b = menuBubble;
    const back = menuBack;
    const active = document.activeElement;
    const lost = !active || active === document.body || msgMenu.contains(active);
    menuBubble = null;
    menuMsg = null;
    menuBack = null;
    b.classList.remove('tg-selected');
    msgMenu.remove();
    menuClosed(closeMenu);
    if (lost && !signal.aborted && back && back.isConnected && back !== document.activeElement) back.focus({ preventScroll: true });
  };
  // openMenu opens the message menu of bubble b, opened via via, at x, y (see
  // placeMenu), closing any other menu on the page.
  const openMenu = (b, via, x, y, above = y, fromRight = false) => {
    const m = messages.get(Number(b.parentNode.dataset.seq));
    if (!m) return;
    const back = via === 'key' ? b : menuBubble ? menuBack : document.activeElement;
    closeMenu();
    menuBubble = b;
    menuMsg = m;
    menuVia = via;
    menuAt = Date.now();
    menuBack = back;
    b.classList.add('tg-selected');
    refreshActions();
    document.body.appendChild(msgMenu);
    placeMenu(x, y, above, fromRight);
    menuOpened(closeMenu);
    if (via !== 'touch') copyItem.focus({ preventScroll: true });
  };
  // choose closes the menu, the focus back where it was (see closeMenu), and
  // runs run with its message.
  const choose = (run) => {
    const m = menuMsg;
    closeMenu();
    if (m) run(m);
  };
  const cancelPress = () => {
    if (!press) return;
    clearTimeout(press.timer);
    press = null;
  };
  // showToast shows text over the bottom of the messages for TOAST_MS.
  const showToast = (text) => {
    if (toastEl) toastEl.remove();
    const el = h('div', { class: 'tg-toast', role: 'status' }, text);
    toastEl = el;
    conv.appendChild(el);
    setTimeout(() => {
      el.remove();
      if (toastEl === el) toastEl = null;
    }, TOAST_MS);
  };
  // copyMessage copies the text of message m, then puts the focus back where
  // it was, which copying through the hidden box moves.
  const copyMessage = async (m) => {
    const back = document.activeElement;
    const ok = await copyText(m.text || '');
    if (back && back.isConnected && document.activeElement !== back) back.focus({ preventScroll: true });
    if (!signal.aborted) showToast(ok ? '已复制' : '复制失败');
  };

  // A right-click on a bubble opens its menu at the pointer. Android fires
  // contextmenu on a long press too: the menu then opens once, where the
  // finger rests, and the contextmenu that follows a long press or the menu
  // key is ignored.
  log.addEventListener('contextmenu', (e) => {
    const b = e.target.closest('.tg-bubble');
    if (!b) return;
    e.preventDefault();
    if (press) {
      clearTimeout(press.timer);
      if (!press.opened) {
        press.opened = true;
        openMenu(press.b, 'touch', press.x, press.y);
      }
      return;
    }
    if (menuBubble === b && menuVia !== 'mouse' && (Date.now() - menuAt < FOLLOW_MS || Date.now() < followUntil)) return;
    openMenu(b, 'mouse', e.clientX, e.clientY);
    followUntil = Date.now() + CLICK_FOLLOW_MS;
  });
  // A right-click, or the Ctrl+click macOS takes for one, leaves the focus
  // where it is, so that closing the menu can give it back.
  log.addEventListener('mousedown', (e) => {
    if ((e.button === 2 || (e.button === 0 && e.ctrlKey)) && e.target.closest('.tg-bubble')) e.preventDefault();
  });
  // A finger resting LONG_PRESS_MS on a bubble opens its menu there; one
  // that lifts, moves more than LONG_PRESS_SLOP or is joined by another does
  // not. Once open, the finger no longer scrolls, and lifting it neither
  // clicks nor closes the menu.
  log.addEventListener('touchstart', (e) => {
    cancelPress();
    const b = e.touches.length === 1 ? e.target.closest('.tg-bubble') : null;
    if (!b) return;
    const t = e.touches[0];
    const p = { b, x: t.clientX, y: t.clientY, opened: false, timer: 0 };
    p.timer = setTimeout(() => {
      if (press !== p || p.opened) return;
      p.opened = true;
      openMenu(b, 'touch', p.x, p.y);
    }, LONG_PRESS_MS);
    press = p;
  }, { passive: true });
  // Not passive: once the menu is open, preventDefault keeps the finger that
  // opened it from dragging the messages under it.
  log.addEventListener('touchmove', (e) => {
    if (!press) return;
    if (press.opened) {
      if (e.cancelable) e.preventDefault();
      return;
    }
    const t = e.touches[0];
    if (Math.hypot(t.clientX - press.x, t.clientY - press.y) > LONG_PRESS_SLOP) cancelPress();
  }, { passive: false });
  log.addEventListener('touchend', (e) => {
    if (press && press.opened) {
      if (e.cancelable) e.preventDefault();
      followUntil = Date.now() + FOLLOW_MS;
    }
    cancelPress();
  }, { passive: false });
  log.addEventListener('touchcancel', cancelPress);
  // A bubble focused becomes the Tab stop. On a focused bubble, ↑ and ↓ move
  // to the one before and after, Home and End to the first and the newest,
  // and the menu key or Shift+F10 opens its menu under it, on the side of the
  // bubble, with the focus on its first item.
  log.addEventListener('focusin', (e) => {
    if (e.target.classList && e.target.classList.contains('tg-bubble')) {
      chosenStop = true;
      setTabStop(e.target);
    }
  });
  log.addEventListener('keydown', (e) => {
    const b = e.target.classList && e.target.classList.contains('tg-bubble') ? e.target : null;
    if (!b) return;
    const all = BUBBLE_KEYS.has(e.key) ? [...log.querySelectorAll('.tg-bubble')] : null;
    if (all) {
      const i = all.indexOf(b);
      const to = { ArrowUp: all[i - 1], ArrowDown: all[i + 1], Home: all[0], End: all[all.length - 1] }[e.key];
      e.preventDefault();
      if (to) to.focus();
      return;
    }
    if (!(e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10'))) return;
    e.preventDefault();
    const r = b.getBoundingClientRect();
    const out = b.parentNode.classList.contains('out');
    openMenu(b, 'key', out ? r.right : r.left, r.bottom + MENU_MARGIN, r.top - MENU_MARGIN, out);
  });
  // In the menu the arrows, Home and End move between the items that can
  // run, Return or Space chooses one, and Tab, like Escape, closes it.
  msgMenu.addEventListener('keydown', (e) => {
    const items = [copyItem, regenItem, withdrawItem].filter((b) => !b.hidden && !b.disabled);
    const n = items.length;
    const i = items.indexOf(document.activeElement);
    let next = null;
    if (e.key === 'ArrowDown') next = items[(i + 1) % n];
    else if (e.key === 'ArrowUp') next = items[(i <= 0 ? n : i) - 1];
    else if (e.key === 'Home') next = items[0];
    else if (e.key === 'End') next = items[n - 1];
    else if (e.key === 'Tab') {
      e.preventDefault();
      closeMenu();
      return;
    }
    if (next) {
      e.preventDefault();
      next.focus();
    }
  });
  msgMenu.addEventListener('contextmenu', (e) => e.preventDefault());
  // A click or a right-click elsewhere, Escape, the user scrolling, a resized
  // window or the end of the pane closes the menu, and any scroll drops a long
  // press. Scrolling is told by what the user does, a wheel, a finger moving
  // (not the one that opened the menu), a scroll key or a press on a
  // scrollbar, and not by scroll events: the pane's own scrolling, as to a
  // new message, and a list drawn afresh fire those too, and leave the menu
  // open.
  const userScroll = (e) => {
    if (menuBubble && !msgMenu.contains(e.target)) closeMenu();
  };
  listen(document, 'wheel', userScroll, signal, true);
  listen(document, 'touchmove', (e) => {
    if (!(press && press.opened)) userScroll(e);
  }, signal, true);
  listen(document, 'keydown', (e) => {
    const t = e.target;
    const typing = t instanceof Element && (t.closest('input, textarea, select') || t.isContentEditable);
    if (SCROLL_KEYS.has(e.key) && !typing) userScroll(e);
  }, signal, true);
  listen(document, 'pointerdown', (e) => {
    const t = e.target;
    const scrolls = t instanceof Element && (t.scrollHeight > t.clientHeight || t.scrollWidth > t.clientWidth);
    if (scrolls && (e.offsetX >= t.clientWidth || e.offsetY >= t.clientHeight)) userScroll(e);
  }, signal, true);
  listen(document, 'click', (e) => {
    if (menuBubble && !msgMenu.contains(e.target) && Date.now() >= followUntil) closeMenu();
  }, signal);
  listen(document, 'contextmenu', (e) => {
    const t = e.target;
    if (menuBubble && !msgMenu.contains(t) && !(log.contains(t) && t.closest('.tg-bubble'))) closeMenu();
  }, signal, true);
  listen(document, 'keydown', (e) => {
    if (e.key === 'Escape' && menuBubble) closeMenu();
  }, signal);
  listen(document, 'scroll', cancelPress, signal, true);
  listen(window, 'resize', closeMenu, signal);
  signal.addEventListener('abort', () => {
    cancelPress();
    closeMenu();
    if (toastEl) toastEl.remove();
  });
  const warn = (text) => h('p', { class: 'tg-warn', role: 'status' }, text);
  const actionError = (err) => {
    if (isAbort(err)) return;
    if (err.status === 404) {
      markGone();
      return;
    }
    if (err.status === 409) {
      mount(sendError, warn(err.code === 'PAUSED' ? err.message : ACTION_BUSY));
      return;
    }
    mount(sendError, errorBox(err));
  };
  // act runs call, a withdraw or regenerate: polling stops first and a poll
  // under way is dropped, then the chat is opened afresh, also when the
  // message was gone already (a 404 MESSAGE_GONE, unlike a 404 of the chat);
  // when call fails otherwise, polling goes on.
  const act = async (call) => {
    acting = true;
    epoch++;
    clearTimeout(timer);
    clearSuggestions();
    mount(sendError);
    refreshActions();
    try {
      await call();
    } catch (err) {
      if (err.status === 404 && err.code === MESSAGE_GONE) {
        reload(warn(MESSAGE_GONE_NOTE));
        return;
      }
      acting = false;
      refreshActions();
      actionError(err);
      if (!gone && !signal.aborted) poll();
      return;
    }
    reload();
  };
  const withdraw = async (seq) => {
    if (botTyping || gone || acting || send.disabled) return;
    const count = [...log.querySelectorAll('.tg-msg')].filter((row) => Number(row.dataset.seq) >= seq).length;
    if (!confirm(`撤回这条及之后共 ${count} 条消息？不能恢复；已经整理进摘要/笔记的内容不会跟着撤回，要彻底清掉用 /忘记我。`)) return;
    await act(() => del(convPath(key, 'messages', seq)));
  };
  const regenerate = async (seq) => {
    if (botTyping || gone || acting || send.disabled) return;
    if (seq !== regenSeq) {
      mount(sendError, warn('这条消息现在不能重新生成。'));
      return;
    }
    await act(() => post(convPath(key, 'messages', seq, 'regenerate')));
  };
  const actions = { withdraw: (m) => withdraw(m.seq), regenerate: (m) => regenerate(m.seq) };

  // place appends message m after the newest: under a date pill when its day
  // differs from the newest message's, otherwise, from the same sender, on
  // the same side, in the newest message's group, which only its last
  // message has a tail of. A fresh message floats in.
  const place = (m, fresh) => {
    const day = dayOf(m.at);
    const newDay = day !== '' && day !== lastDay;
    if (newDay) {
      log.appendChild(h('li', { class: 'tg-day' }, h('span', null, dayLabel(m.at))));
      lastDay = day;
    }
    const el = bubble(m);
    messages.set(m.seq, m);
    if (lastEl && !newDay && last.from_bot === m.from_bot && last.sender_id === m.sender_id) {
      lastEl.classList.remove('tail');
      el.classList.add('cont');
    }
    el.classList.add('tail');
    if (fresh) {
      el.classList.add('is-new');
      el.addEventListener('animationend', (e) => {
        if (e.target === el) el.classList.remove('is-new');
      });
    }
    log.appendChild(el);
    lastEl = el;
    if (!chosenStop) setTabStop(el.firstChild);
  };

  // add appends the rows not shown yet, in place of any suggestions, hands
  // them to onMessages and reports whether there were any; stick scrolls them
  // into view even when the user has scrolled up, and fresh, for rows that
  // just arrived, has them float in. A row is shown once, by its seq.
  const add = (rows, stick, fresh) => {
    const atBottom = nearBottom();
    const added = [];
    for (const m of rows) {
      if (m.seq <= after) continue;
      if (!added.length) clearSuggestions();
      place(m, fresh);
      after = m.seq;
      last = m;
      added.push(m);
    }
    if (!added.length) return false;
    refreshActions();
    if (stick || atBottom) log.scrollTop = log.scrollHeight;
    if (onMessages && !signal.aborted) onMessages(added);
    return true;
  };

  const first = await get(convPath(key, 'messages'), { limit: HISTORY }, signal);
  add((first.messages || []).slice().reverse(), true, false);

  let timer = 0;
  let running = false;
  let again = false;
  let stick = false;
  const schedule = (ms) => {
    clearTimeout(timer);
    if (!signal.aborted && !gone) timer = setTimeout(poll, ms);
  };
  const markGone = () => {
    gone = true;
    regenSeq = 0;
    clearTimeout(timer);
    clearSuggestions();
    showTyping(false);
    showScene(null);
    input.disabled = true;
    send.disabled = true;
    refreshActions();
    mount(sendError);
    mount(pollError, errorBox({ message: GONE }));
  };
  // wanted reports whether suggestions after the message seq would still be
  // shown: the switch is on, the bot's turn is done with seq its newest
  // message, the user has not answered it yet and the box is empty.
  const wanted = (seq) => suggestOn && !gone && !signal.aborted && !botTyping &&
    last !== null && last.from_bot && last.seq === seq && seq !== sentAfter && !input.value.trim();
  // suggest asks, once per message, for suggestions after the newest message
  // when they are wanted, and shows them, a full-width button each, unless
  // meanwhile they no longer are. A failure shows nothing.
  const suggest = async () => {
    if (last === null || asked === last.seq || !wanted(last.seq)) return;
    const seq = last.seq;
    asked = seq;
    let res;
    try {
      res = await post(convPath(key, 'suggestions'), { after_seq: seq });
    } catch {
      return;
    }
    const items = (res && res.suggestions) || [];
    if (!items.length || !wanted(seq)) return;
    const atBottom = nearBottom();
    mount(chips, items.map((text) => h('button', { type: 'button', onclick: () => submit(text) }, text)));
    log.appendChild(chips);
    if (atBottom) log.scrollTop = log.scrollHeight;
  };
  async function poll() {
    if (gone || acting) return;
    if (running) {
      again = true;
      return;
    }
    running = true;
    clearTimeout(timer);
    let next = POLL_MS;
    const at = epoch;
    let stale = false;
    try {
      const res = await get(convPath(key, 'chat'), { after }, signal);
      if (at !== epoch) {
        // A withdraw or regenerate started meanwhile: what this poll read
        // may be deleted by now.
        stale = true;
      } else {
        const rows = res.messages || [];
        // A full page catches up on a backlog rather than brings news: it
        // does not float in.
        if (add(rows, stick, rows.length < PAGE)) stick = false;
        botTyping = !!res.typing;
        regenSeq = res.regenerate_seq || 0;
        showTyping(botTyping);
        showScene(res.scene);
        refreshActions();
        mount(pollError);
        if (rows.length >= PAGE) next = 0;
        else suggest();
      }
    } catch (err) {
      if (isAbort(err) || signal.aborted) return;
      if (err.status === 404) {
        markGone();
        return;
      }
      mount(pollError, errorBox(err));
    } finally {
      running = false;
    }
    if (acting) return;
    if (again || stale) {
      again = false;
      next = 0;
    }
    schedule(next);
  }
  signal.addEventListener('abort', () => clearTimeout(timer));

  // grow fits the box to its text, up to MAX_LINES lines, then scrolls it;
  // messages scrolled to the bottom stay there. A resized window, which may
  // wrap the text anew, fits it again.
  const grow = () => {
    if (!input.isConnected) return;
    const atBottom = nearBottom();
    const cs = getComputedStyle(input);
    const sum = (a, b) => parseFloat(cs[a]) + parseFloat(cs[b]);
    const borders = sum('borderTopWidth', 'borderBottomWidth');
    const max = parseFloat(cs.lineHeight) * MAX_LINES + sum('paddingTop', 'paddingBottom') + borders;
    input.style.height = 'auto';
    const full = input.scrollHeight + borders;
    input.style.height = `${Math.min(full, max)}px`;
    input.style.overflowY = full > max ? 'auto' : 'hidden';
    if (atBottom) log.scrollTop = log.scrollHeight;
  };

  // submit sends text, typed or a suggestion, as the user; suggestions for
  // the message it answers are no longer shown. Text that fails to send is
  // left in the box, a suggestion's too, unless the box has other text.
  const submit = async (text) => {
    if (!text.trim() || send.disabled || gone || acting) return;
    if (last !== null) sentAfter = last.seq;
    clearSuggestions();
    send.disabled = true;
    refreshActions();
    mount(sendError);
    try {
      const res = await post(convPath(key, 'messages'), { text });
      if (input.value === text) input.value = '';
      if (res && res.suppressed_reason) mount(sendError, suppressedNote(res.suppressed_reason));
      stick = true;
      poll();
    } catch (err) {
      if (isAbort(err)) return;
      if (err.status === 404) {
        markGone();
      } else {
        mount(sendError, errorBox(err));
        if (!input.value.trim()) input.value = text;
      }
    } finally {
      send.disabled = gone;
      refreshActions();
      grow();
      if (!gone) input.focus();
    }
  };
  // The command menu comes before Return sends, which it stops while open;
  // the suggestions under the messages hide behind it meanwhile.
  const commands = commandMenu({
    key, input, signal, send: submit,
    onToggle: (open) => {
      chips.hidden = open;
    },
  });
  if (!touch) {
    input.addEventListener('keydown', (e) => {
      // Safari ends an IME composition before the keydown of the Enter that
      // commits it; that keydown has keyCode 229.
      if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) {
        e.preventDefault();
        submit(input.value);
      }
    });
  }
  input.addEventListener('input', clearSuggestions);
  input.addEventListener('input', grow);
  listen(window, 'resize', grow, signal);
  suggestItem.addEventListener('click', () => {
    suggestOn = !suggestOn;
    saveSuggestionsOn(suggestOn);
    showSwitch();
    clearSuggestions();
    asked = 0;
    suggest();
  });
  const form = h('form', {
    class: 'tg-compose',
    onsubmit: (e) => {
      e.preventDefault();
      submit(input.value);
    },
  }, commands, input, send);

  const conv = h('section', { class: 'tg-conv' }, log, pollError, sendError, form);

  poll();
  requestAnimationFrame(() => {
    log.scrollTop = log.scrollHeight;
  });
  return [
    chatBar({ key, bot, title, status, items: [suggestItem], onBack, onDelete, signal }),
    conv,
  ];
}
