import { h, mount, errorBox } from '../lib/dom.js';
import { get, post, convPath, isAbort } from '../lib/api.js';
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

// chatPane talks to the bot in the panel chat key, as the panel user. It
// opens with the latest messages, then polls GET chat with the newest seq it
// has seen until signal fires or the chat is gone (404), which stops the
// polling and the sending. hint, when given, is shown above the chat.
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
  let after = 0;

  // add appends the rows not shown yet and reports whether there were any;
  // stick scrolls them into view even when the user has scrolled up.
  const add = (rows, stick) => {
    const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
    let added = false;
    for (const m of rows) {
      if (m.seq <= after) continue;
      log.appendChild(bubble(m));
      after = m.seq;
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
    typing.hidden = true;
    input.disabled = true;
    send.disabled = true;
    mount(sendError);
    mount(pollError, errorBox({ message: GONE }));
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
      typing.hidden = !res.typing;
      mount(pollError);
      if (rows.length >= PAGE) next = 0;
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

  const submit = async () => {
    const text = input.value;
    if (!text.trim() || send.disabled || gone) return;
    send.disabled = true;
    mount(sendError);
    try {
      await post(convPath(key, 'messages'), { text });
      if (input.value === text) input.value = '';
      stick = true;
      poll();
    } catch (err) {
      if (isAbort(err)) return;
      if (err.status === 404) markGone();
      else mount(sendError, errorBox(err));
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
        submit();
      }
    });
  }
  const form = h('form', {
    class: 'chat-form',
    onsubmit: (e) => {
      e.preventDefault();
      submit();
    },
  }, input, send);

  poll();
  requestAnimationFrame(() => {
    log.scrollTop = log.scrollHeight;
  });
  return [
    hint ? h('p', { class: 'hint' }, hint) : null,
    h('section', { class: 'chat' }, log, typing, pollError, form, sendError),
  ];
}
