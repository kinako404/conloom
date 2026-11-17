// The command menu of both chat panes (views/chat.js, views/chatclassic.js),
// like the bot command menu of Telegram: typing the chat's command prefix
// lists the commands GET commands offers in that chat.

import { h, mount } from '../lib/dom.js';
import { get, convPath } from '../lib/api.js';
import { listen } from './chat.js';

// CACHE_MS is how long the commands of a chat are kept for the next time it
// opens.
const CACHE_MS = 60000;
// CARET_KEYS move the caret without changing the text.
const CARET_KEYS = new Set(['ArrowLeft', 'ArrowRight', 'Home', 'End']);

// cached holds, by chat key, {at, list}: the promise of the chat's {prefix,
// commands} and when it was asked for; a failed one is dropped.
const cached = new Map();
let menus = 0;

function loadCommands(key) {
  const hit = cached.get(key);
  if (hit && Date.now() - hit.at < CACHE_MS) return hit.list;
  const list = get(convPath(key, 'commands')).then((res) => ({
    prefix: res && typeof res.prefix === 'string' ? res.prefix : '',
    commands: res && Array.isArray(res.commands) ? res.commands : [],
  }));
  cached.set(key, { at: Date.now(), list });
  list.catch(() => {
    const now = cached.get(key);
    if (now && now.list === list) cached.delete(key);
  });
  return list;
}

// matchCommands lists the commands whose name or an alias starts with query,
// then those where one contains it, ignoring case.
export function matchCommands(commands, query) {
  const q = query.toLowerCase();
  const first = [];
  const then = [];
  for (const c of commands) {
    const words = [c.name, ...(c.aliases || [])].map((w) => String(w).toLowerCase());
    if (words.some((w) => w.startsWith(q))) first.push(c);
    else if (words.some((w) => w.includes(q))) then.push(c);
  }
  return first.concat(then);
}

// commandMenu adds the command menu to input, the message box of the chat
// key, and returns its popup, which the pane puts in the box's form, above
// the box. While the whole text is the chat's prefix and one word, with no
// whitespace anywhere, the popup lists the commands that match that word
// (see matchCommands). ↑ and ↓ move the highlight, Return or Tab picks
// the highlighted command, as does a click or a tap; Escape, or a click
// elsewhere, closes it and keeps the text, and it opens again once the text
// changes. A command without arguments is sent at once through send(text),
// the pane's own sending; one with arguments fills the box with it and a
// space. While the popup is open Return only picks, never sends the text.
// onToggle(open) tells when it opens and closes. The commands are asked for
// once when the pane opens, then kept for CACHE_MS; when that fails there is
// no menu. Its listeners end with signal.
export function commandMenu({ key, input, signal, send, onToggle = () => {} }) {
  const id = `cmd-menu-${++menus}`;
  const box = h('div', { class: 'cmd-menu', role: 'listbox', id, 'aria-label': '命令', hidden: true });
  let prefix = '';
  let commands = null;
  let shown = [];
  let active = -1;
  // dismissed is the text the menu was closed on by Escape or a pick; it
  // stays closed until the text changes.
  let dismissed = null;
  input.setAttribute('aria-haspopup', 'listbox');
  input.setAttribute('aria-autocomplete', 'list');
  input.setAttribute('aria-controls', id);
  input.setAttribute('aria-expanded', 'false');

  const close = () => {
    if (box.hidden) return;
    box.hidden = true;
    mount(box);
    shown = [];
    active = -1;
    input.setAttribute('aria-expanded', 'false');
    input.removeAttribute('aria-activedescendant');
    onToggle(false);
  };
  // highlight marks the row i and scrolls the popup to show it.
  const highlight = (i) => {
    active = i;
    const rows = box.children;
    for (let j = 0; j < rows.length; j++) rows[j].setAttribute('aria-selected', String(j === i));
    const row = rows[i];
    if (!row) return;
    input.setAttribute('aria-activedescendant', row.id);
    if (row.offsetTop < box.scrollTop) box.scrollTop = row.offsetTop;
    else if (row.offsetTop + row.offsetHeight > box.scrollTop + box.clientHeight) {
      box.scrollTop = row.offsetTop + row.offsetHeight - box.clientHeight;
    }
  };
  // word is the text after the prefix when the whole text is the prefix and
  // one word, with the caret after the prefix, else null. Text with any
  // whitespace, such as a command and its arguments, gets no menu, so a pick
  // never replaces arguments already typed.
  const word = () => {
    const text = input.value;
    if (!commands || !prefix || input.disabled || !text.startsWith(prefix) || /\s/.test(text)) return null;
    if (input.selectionStart < prefix.length) return null;
    return text.slice(prefix.length);
  };
  const pick = (c) => {
    if (!c || input.disabled) return;
    const text = prefix + c.name;
    close();
    input.value = c.args ? `${text} ` : text;
    dismissed = input.value;
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
    input.dispatchEvent(new Event('input', { bubbles: true }));
    if (!c.args) send(text);
  };
  const row = (c, i) => {
    const name = prefix + c.name;
    const usage = String(c.usage || '');
    const hint = usage.startsWith(name) ? usage.slice(name.length).trim() : usage;
    return h('div', {
      class: 'cmd-item', role: 'option', id: `${id}-${i}`, 'aria-selected': 'false',
      // Keeps the focus, and an on-screen keyboard, in the box.
      onmousedown: (e) => e.preventDefault(),
      onmousemove: () => {
        if (active !== i) highlight(i);
      },
      onclick: () => pick(c),
    },
    h('strong', { class: 'cmd-name' }, name),
    hint ? h('span', { class: 'cmd-args' }, hint) : null,
    h('span', { class: 'cmd-desc' }, c.description || ''));
  };
  const update = () => {
    if (signal.aborted) return;
    if (input.value !== dismissed) dismissed = null;
    const q = dismissed === null ? word() : null;
    const list = q === null ? [] : matchCommands(commands, q);
    if (!list.length) {
      close();
      return;
    }
    if (!box.hidden && list.length === shown.length && list.every((c, i) => c === shown[i])) return;
    shown = list;
    mount(box, list.map(row));
    const opening = box.hidden;
    box.hidden = false;
    input.setAttribute('aria-expanded', 'true');
    box.scrollTop = 0;
    highlight(0);
    if (opening) onToggle(true);
  };

  // In the capture phase, so that it comes before the pane's own Return to
  // send, which it stops while the popup is open.
  listen(input, 'keydown', (e) => {
    if (box.hidden || e.isComposing || e.keyCode === 229) return;
    const n = shown.length;
    if (e.key === 'ArrowDown') highlight((active + 1) % n);
    else if (e.key === 'ArrowUp') highlight((active - 1 + n) % n);
    else if ((e.key === 'Enter' || e.key === 'Tab') && !e.shiftKey) pick(shown[active] || shown[0]);
    else if (e.key === 'Escape') {
      dismissed = input.value;
      close();
    } else return;
    e.preventDefault();
    e.stopImmediatePropagation();
  }, signal, true);
  listen(input, 'input', update, signal);
  listen(input, 'focus', update, signal);
  listen(input, 'click', update, signal);
  listen(input, 'keyup', (e) => {
    if (CARET_KEYS.has(e.key)) update();
  }, signal);
  listen(document, 'selectionchange', () => {
    if (document.activeElement === input) update();
  }, signal);
  listen(input, 'focusout', (e) => {
    if (!box.contains(e.relatedTarget)) close();
  }, signal);
  listen(document, 'pointerdown', (e) => {
    if (!box.hidden && e.target !== input && !box.contains(e.target)) close();
  }, signal);
  signal.addEventListener('abort', close);

  loadCommands(key).then((res) => {
    if (signal.aborted) return;
    prefix = res.prefix;
    commands = res.commands;
    if (document.activeElement === input) update();
  }, () => {});
  return box;
}
