import { h, mount, errorBox } from '../lib/dom.js';
import { get, post, del, convPath, isAbort } from '../lib/api.js';
import { fmtTime } from '../lib/format.js';

// POLL_MS is how often the chat asks for new messages.
const POLL_MS = 1500;
// HISTORY is how many recent messages the chat opens with.
const HISTORY = 50;
// PAGE is the most messages one GET chat returns (maxChatMessages); a full
// page is followed by another poll right away.
const PAGE = 200;
// TOUCH matches a device whose main pointer is a finger. Its on-screen
// keyboard has no Shift+Return, so Return types a newline and 发送 sends.
const TOUCH = '(pointer: coarse)';
// SUGGEST_KEY is where the browser keeps the 建议回复 switch, which is on
// unless set to SUGGEST_OFF.
const SUGGEST_KEY = 'conloom.chat.suggestions';
const SUGGEST_OFF = 'off';

// suggestionsOn reads the 建议回复 switch; a browser that refuses storage
// gets it on.
function suggestionsOn() {
  try {
    return localStorage.getItem(SUGGEST_KEY) !== SUGGEST_OFF;
  } catch {
    return true;
  }
}

function saveSuggestionsOn(on) {
  try {
    if (on) localStorage.removeItem(SUGGEST_KEY);
    else localStorage.setItem(SUGGEST_KEY, SUGGEST_OFF);
  } catch {
    // Without storage the switch lasts as long as the page.
  }
}

function bubble(m) {
  return h('li', { class: m.from_bot ? 'bubble bot' : 'bubble user' },
    h('div', { class: 'meta' },
      h('strong', null, m.sender_name || m.sender_id),
      h('span', null, fmtTime(m.at)),
      m.channel === 'panel' ? h('span', { class: 'badge' }, '面板') : null),
    h('div', { class: 'body' }, m.text));
}

// GONE is shown, once, when the chat no longer exists.
const GONE = '这个聊天已不存在，可能已经被删除。';

// SUPPRESSED says why a sent message, stored all the same, gets no reply, by
// the suppressed_reason of the 202 to POST messages.
const SUPPRESSED = {
  paused: '这个聊天暂停中，bot 不会回复这条消息。',
  rate_limited: '发得太快了，bot 不会回复这条消息，稍后再发。',
  queue_full: '待回复的消息太多，bot 不会回复这条消息，稍后再发。',
  stale: '这条消息到得太晚，bot 不会回复。',
};

function suppressedNote(reason) {
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

// chatPane talks to the bot in the panel chat key, as the panel user. It
// opens with the latest messages, then polls GET chat with the newest seq it
// has seen until signal fires or the chat is gone (404), which stops the
// polling and the sending. With the 建议回复 switch on, once the bot's turn is
// done it offers reply suggestions under the newest message, which send
// themselves when clicked. hint, when given, is shown above the chat. bot
// ({id, name, avatar}) is who the chat is with, onBack() returns to the chat
// list and onDelete() deletes the chat after confirmation, for the pane's top
// bar.
export async function chatPane({ key, signal, hint }) {
  const log = h('ul', { class: 'chat-log' });
  const typing = h('div', { class: 'chat-typing', hidden: true }, '对方正在输入…');
  const pollError = h('div');
  const sendError = h('div');
  const touch = matchMedia(TOUCH).matches;
  const input = h('textarea', {
    class: 'chat-input', rows: 3, placeholder: touch ? '输入消息' : '输入消息，回车发送，Shift+回车换行',
  });
  const send = h('button', { type: 'submit', class: 'primary' }, '发送');
  const toggle = h('input', { type: 'checkbox', checked: suggestionsOn() });
  // chips holds the suggestions; it is in the log, after the newest message,
  // only while it shows some.
  const chips = h('li', { class: 'chat-suggestions' });
  let after = 0;
  // last is the newest message shown, botTyping what the last poll said,
  // asked the seq suggestions were last asked for and sentAfter the seq of
  // the newest message when the user last sent one.
  let last = null;
  let botTyping = true;
  let asked = 0;
  let sentAfter = 0;

  const nearBottom = () => log.scrollHeight - log.scrollTop - log.clientHeight < 40;
  const clearSuggestions = () => {
    mount(chips);
    chips.remove();
  };

  // add appends the rows not shown yet, in place of any suggestions, and
  // reports whether there were any; stick scrolls them into view even when
  // the user has scrolled up.
  const add = (rows, stick) => {
    const atBottom = nearBottom();
    let added = false;
    for (const m of rows) {
      if (m.seq <= after) continue;
      if (!added) clearSuggestions();
      log.appendChild(bubble(m));
      after = m.seq;
      last = m;
      added = true;
    }
    if (added && (stick || atBottom)) log.scrollTop = log.scrollHeight;
    return added;
  };

  const first = await get(convPath(key, 'messages'), { limit: HISTORY }, signal);
  add((first.messages || []).slice().reverse(), true);

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
    typing.hidden = true;
    input.disabled = true;
    send.disabled = true;
    mount(sendError);
    mount(pollError, errorBox({ message: GONE }));
  };
  // wanted reports whether suggestions after the message seq would still be
  // shown: the switch is on, the bot's turn is done with seq its newest
  // message, the user has not answered it yet and the box is empty.
  const wanted = (seq) => toggle.checked && !gone && !signal.aborted && !botTyping &&
    last !== null && last.from_bot && last.seq === seq && seq !== sentAfter && !input.value.trim();
  // suggest asks, once per message, for suggestions after the newest message
  // when they are wanted, and shows them unless meanwhile they no longer are.
  // A failure shows nothing.
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
      if (add(rows, stick)) stick = false;
      botTyping = !!res.typing;
      typing.hidden = !botTyping;
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
  toggle.addEventListener('change', () => {
    saveSuggestionsOn(toggle.checked);
    clearSuggestions();
    asked = 0;
    suggest();
  });
  const form = h('form', {
    class: 'chat-form',
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
    hint ? h('p', { class: 'hint' }, hint) : null,
    h('section', { class: 'chat' }, log, typing, pollError, form, sendError,
      h('label', { class: 'chat-options' }, toggle, '建议回复')),
  ];
}
