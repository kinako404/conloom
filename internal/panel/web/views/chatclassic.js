// The conversation pane of the classic chat page (views/chatsclassic.js),
// kept for debugging beside the Telegram-style one in views/chat.js, whose
// constants and helpers it shares.

import { h, mount, errorBox } from '../lib/dom.js';
import { get, post, convPath, isAbort } from '../lib/api.js';
import { fmtTime } from '../lib/format.js';
import { POLL_MS, HISTORY, PAGE, TOUCH, GONE, suggestionsOn, saveSuggestionsOn, suppressedNote } from './chat.js';

function bubble(m) {
  return h('li', { class: m.from_bot ? 'bubble bot' : 'bubble user' },
    h('div', { class: 'meta' },
      h('strong', null, m.sender_name || m.sender_id),
      h('span', null, fmtTime(m.at)),
      m.channel === 'panel' ? h('span', { class: 'badge' }, '面板') : null),
    h('div', { class: 'body' }, m.text));
}

// chatPane talks to the bot in the panel chat key, as the panel user. It
// opens with the latest messages, then polls GET chat with the newest seq it
// has seen until signal fires or the chat is gone (404), which stops the
// polling and the sending. With the 建议回复 switch on, once the bot's turn is
// done it offers reply suggestions under the newest message, which send
// themselves when clicked. hint, when given, is shown above the chat.
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
