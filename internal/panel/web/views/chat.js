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

function bubble(m) {
  return h('li', { class: m.from_bot ? 'bubble bot' : 'bubble user' },
    h('div', { class: 'meta' },
      h('strong', null, m.sender_name || m.sender_id),
      h('span', null, fmtTime(m.at)),
      m.channel === 'panel' ? h('span', { class: 'badge' }, '面板') : null),
    h('div', { class: 'body' }, m.text));
}

// chatTab talks to the bot as the conversation's user. It opens with the
// latest messages, then polls GET chat with the newest seq it has seen until
// the page is left.
export async function chatTab({ key, signal }) {
  const log = h('ul', { class: 'chat-log' });
  const typing = h('div', { class: 'chat-typing', hidden: true }, '对方正在输入…');
  const pollError = h('div');
  const sendError = h('div');
  const input = h('textarea', { class: 'chat-input', rows: 3, placeholder: '输入消息，回车发送，Shift+回车换行' });
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
  const schedule = (ms) => {
    clearTimeout(timer);
    if (!signal.aborted) timer = setTimeout(poll, ms);
  };
  async function poll() {
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
    if (!text.trim() || send.disabled) return;
    send.disabled = true;
    mount(sendError);
    try {
      await post(convPath(key, 'messages'), { text });
      if (input.value === text) input.value = '';
      stick = true;
      poll();
    } catch (err) {
      if (!isAbort(err)) mount(sendError, errorBox(err));
    } finally {
      send.disabled = false;
      input.focus();
    }
  };
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      submit();
    }
  });
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
    h('p', { class: 'hint' }, '以这个会话的用户身份和 bot 聊天。从这里发的消息带“面板”标记，bot 的回复只出现在面板里，不会发到原平台。每条最多 2000 字。'),
    h('section', { class: 'chat' }, log, typing, pollError, form, sendError),
  ];
}
