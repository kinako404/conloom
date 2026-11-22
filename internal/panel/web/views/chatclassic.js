// The conversation pane of the classic chat page (views/chatsclassic.js),
// kept for debugging beside the Telegram-style one in views/chat.js, whose
// constants and helpers it shares.

import { h, mount, errorBox } from '../lib/dom.js';
import { get, post, del, convPath, isAbort } from '../lib/api.js';
import { go, href } from '../lib/router.js';
import { fmtTime } from '../lib/format.js';
import {
  imageStrip,
  POLL_MS, HISTORY, PAGE, TOUCH, GONE, MESSAGE_GONE, MESSAGE_GONE_NOTE, ACTION_BUSY, suggestionsOn, saveSuggestionsOn, suppressedNote,
} from './chat.js';
import { commandMenu } from './commandmenu.js';

// IMAGES are the classes of a message's images on this page (see
// imageStrip).
const IMAGES = { root: 'chat-classic', strip: 'chat-images', image: 'chat-image', viewer: 'chat-viewer' };

// bubble shows message m of the chat key, its images (see imageStrip; signal
// closes their viewer) above its text, with 撤回 and, for the bot's,
// 重新生成 (hidden until refreshActions shows it), which call
// actions.withdraw(m) and actions.regenerate(m).
function bubble(key, m, actions, signal) {
  return h('li', { class: m.from_bot ? 'bubble bot' : 'bubble user', dataset: { seq: String(m.seq) } },
    h('div', { class: 'meta' },
      h('strong', null, m.sender_name || m.sender_id),
      h('span', null, fmtTime(m.at)),
      m.channel === 'panel' ? h('span', { class: 'badge' }, '面板') : null),
    imageStrip(key, m, signal, IMAGES),
    m.text ? h('div', { class: 'body' }, m.text) : null,
    h('div', { class: 'chat-actions' },
      h('button', { type: 'button', class: 'chat-action danger', onclick: () => actions.withdraw(m) }, '撤回'),
      m.from_bot ? h('button', { type: 'button', class: 'chat-action', 'data-regenerate': '1', onclick: () => actions.regenerate(m) }, '重新生成') : null));
}

// chatPane talks to the bot in the panel chat key, as the panel user. It
// opens with the latest messages, then polls GET chat with the newest seq it
// has seen until signal fires or the chat is gone (404), which stops the
// polling and the sending. With the 建议回复 switch on, once the bot's turn is
// done it offers reply suggestions under the newest message, which send
// themselves when clicked. hint, when given, is shown above the chat. Typing
// the command prefix in the box opens the command menu (see commandMenu).
// Each message can be withdrawn (撤回, with what follows it), and the one GET
// chat names in regenerate_seq regenerated (重新生成); both are off while the
// bot is busy or a message is being sent. Polling stops before either is
// asked for and a poll already under way is dropped, so no message they
// delete is shown again; once done, reload(note) opens the chat afresh (by
// default the route of key), with note, when given, shown there: a message
// that is gone already reloads with MESSAGE_GONE_NOTE, a chat that is gone
// stops the pane as a 404 of the polling does.
export async function chatPane({ key, signal, hint, reload = () => go(href('chat', key)) }) {
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
  // last is the newest message shown, botTyping and regenSeq what the last
  // poll said, asked the seq suggestions were last asked for and sentAfter
  // the seq of the newest message when the user last sent one. acting is set
  // while a withdraw or regenerate is under way, and epoch counts those
  // started: a poll that began before one is dropped.
  let last = null;
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

  const refreshActions = () => {
    const off = botTyping || gone || acting || send.disabled;
    for (const row of log.querySelectorAll('.bubble')) {
      const regen = row.querySelector('[data-regenerate]');
      if (regen) regen.hidden = !regenSeq || Number(row.dataset.seq) !== regenSeq;
      for (const b of row.querySelectorAll('.chat-action')) b.disabled = off;
    }
  };
  const warn = (text) => h('p', { class: 'hint warn', role: 'status' }, text);
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
    const count = [...log.querySelectorAll('.bubble')].filter((row) => Number(row.dataset.seq) >= seq).length;
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

  // add appends the rows not shown yet, in place of any suggestions, and
  // reports whether there were any; stick scrolls them into view even when
  // the user has scrolled up.
  const add = (rows, stick) => {
    const atBottom = nearBottom();
    let added = false;
    for (const m of rows) {
      if (m.seq <= after) continue;
      if (!added) clearSuggestions();
      log.appendChild(bubble(key, m, actions, signal));
      after = m.seq;
      last = m;
      added = true;
    }
    if (added) refreshActions();
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
    if (!signal.aborted && !gone) timer = setTimeout(poll, ms);
  };
  const markGone = () => {
    gone = true;
    regenSeq = 0;
    clearTimeout(timer);
    clearSuggestions();
    typing.hidden = true;
    input.disabled = true;
    send.disabled = true;
    refreshActions();
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
        if (add(rows, stick)) stick = false;
        botTyping = !!res.typing;
        regenSeq = res.regenerate_seq || 0;
        typing.hidden = !botTyping;
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
  }, commands, input, send);

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
