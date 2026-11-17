import { h, mount, errorBox } from '../lib/dom.js';
import { get, post, del, convPath, isAbort } from '../lib/api.js';
import { href } from '../lib/router.js';
import { fmtTime } from '../lib/format.js';
import { avatarEl } from './avatar.js';

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
// channel only when it is not the panel.
function bubble(m) {
  const when = fmtTime(m.at);
  return h('li', { class: m.from_bot ? 'tg-msg in' : 'tg-msg out' },
    h('div', { class: 'tg-bubble' },
      m.channel && m.channel !== 'panel' ? h('div', null, h('span', { class: 'badge' }, m.channel)) : null,
      h('span', { class: 'tg-text' }, m.text),
      h('span', { class: 'tg-time', title: m.sender_name ? `${m.sender_name} · ${when}` : when }, clock(m.at))));
}

// listen adds fn as a listener of type on target until signal fires.
function listen(target, type, fn, signal) {
  target.addEventListener(type, fn);
  signal.addEventListener('abort', () => target.removeEventListener(type, fn));
}

// GONE is shown, once, when the chat no longer exists.
export const GONE = '这个聊天已不存在，可能已经被删除。';

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
// status, when given, under the name; and a ⋮ menu with 会话详情, items and,
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
  return h('header', { class: 'tg-bar' },
    h('button', { type: 'button', class: 'tg-bar-back', title: '返回', 'aria-label': '返回', onclick: onBack }, '←'),
    bot ? avatarEl(bot, 40) : null,
    h('div', { class: 'tg-bar-name', title: title || null },
      h('strong', null, bot ? bot.name || bot.id : key),
      status || h('span', { class: 'tg-bar-status' })),
    h('div', { class: 'tg-bar-actions' }, more, menu));
}

// chatPane talks to the bot in the panel chat key, as the panel user, and
// returns its top bar (see chatBar) followed by the conversation. It opens
// with the latest messages, then polls GET chat with the newest seq it has
// seen until signal fires or the chat is gone (404), which stops the polling
// and the sending. Messages are bubbles grouped by sender, with a date pill
// where the day changes; those that arrive after the opening ones float in,
// and 正在输入 shows under the name while the bot is busy. With the 建议回复 switch on, once the bot's turn is
// done it offers reply suggestions under the newest message, which send
// themselves when clicked. hint, when given, is shown above the messages.
// bot ({id, name, avatar}) is who the chat is with, title who the bot talks
// with; onBack() returns to the chat list and onDelete() deletes the chat
// after confirmation. onMessages(messages), when given, gets the messages
// each time some are shown: the latest at first, then each one new.
export async function chatPane({ key, signal, hint, bot, title, onBack, onDelete, onMessages }) {
  const log = h('ul', { class: 'tg-log' }, hint ? h('li', { class: 'tg-note' }, hint) : null);
  // typing is 正在输入 with three blinking dots, always in the status line,
  // which keeps its height, and shown by showTyping, which fades it.
  const typing = h('span', { class: 'tg-typing', 'aria-hidden': 'true' }, '正在输入',
    h('span', { class: 'tg-typing-dot' }), h('span', { class: 'tg-typing-dot' }), h('span', { class: 'tg-typing-dot' }));
  const status = h('span', { class: 'tg-bar-status' }, typing);
  const showTyping = (on) => {
    typing.classList.toggle('on', on);
    typing.setAttribute('aria-hidden', String(!on));
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
  // newest day with a date pill; botTyping is what the last poll said, asked
  // the seq suggestions were last asked for and sentAfter the seq of the
  // newest message when the user last sent one.
  let last = null;
  let lastEl = null;
  let lastDay = '';
  let botTyping = true;
  let asked = 0;
  let sentAfter = 0;

  const nearBottom = () => log.scrollHeight - log.scrollTop - log.clientHeight < 40;
  const clearSuggestions = () => {
    mount(chips);
    chips.remove();
  };

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
  let gone = false;
  const schedule = (ms) => {
    clearTimeout(timer);
    if (!signal.aborted && !gone) timer = setTimeout(poll, ms);
  };
  const markGone = () => {
    gone = true;
    clearTimeout(timer);
    clearSuggestions();
    showTyping(false);
    input.disabled = true;
    send.disabled = true;
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
    if (gone) return;
    if (running) {
      again = true;
      return;
    }
    running = true;
    clearTimeout(timer);
    let next = POLL_MS;
    try {
      const res = await get(convPath(key, 'chat'), { after }, signal);
      const rows = res.messages || [];
      // A full page catches up on a backlog rather than brings news: it
      // does not float in.
      if (add(rows, stick, rows.length < PAGE)) stick = false;
      botTyping = !!res.typing;
      showTyping(botTyping);
      mount(pollError);
      if (rows.length >= PAGE) next = 0;
      else suggest();
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
    if (again) {
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
    if (!text.trim() || send.disabled || gone) return;
    if (last !== null) sentAfter = last.seq;
    clearSuggestions();
    send.disabled = true;
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
      grow();
      if (!gone) input.focus();
    }
  };
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
  }, input, send);

  poll();
  requestAnimationFrame(() => {
    log.scrollTop = log.scrollHeight;
  });
  return [
    chatBar({ key, bot, title, status, items: [suggestItem], onBack, onDelete, signal }),
    h('section', { class: 'tg-conv' }, log, pollError, sendError, form),
  ];
}
