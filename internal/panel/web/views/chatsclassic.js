// The classic chat page, kept for debugging beside the Telegram-style one in
// views/chats.js, whose dialogs it shares. It sits in the panel's frame, under
// the top bar, at #/chat; its styles hang on its root's class chat-classic.

import { h, mount, errorBox, loading } from '../lib/dom.js';
import { get, convPath, isAbort } from '../lib/api.js';
import { go, href } from '../lib/router.js';
import { fmtTime, fmtClock } from '../lib/format.js';
import { PANEL, PANEL_HINT, newChatDialog, botDialog, configBotDialog, deleteBotDialog } from './chats.js';
import { deleteChatConfirmed } from './chat.js';
import { chatPane } from './chatclassic.js';

// NARROW is the media query under which style.css puts the chat list above
// the chat.
const NARROW = '(max-width: 699px)';

// chatHead names the chat in the pane: who the bot talks with, the bot and a
// link to the conversation's page.
function chatHead(key, title, botName) {
  return h('div', { class: 'chat-head' },
    h('h2', null, title),
    h('span', { class: 'muted' }, botName),
    h('span', { class: 'spacer' }),
    h('a', { href: href('conversations', key) }, '会话详情'));
}

// chatPage lays out the chat page: the bots with their panel chats on the
// left, the chat chosen on the right; on a narrow screen the list is above
// the chat. A link beside the heading opens the Telegram-style page on the
// same chat. load reads the list; select(key, signal) shows the chat key,
// scrolled into view on a narrow screen, or a hint when key is empty, and
// keeps the list.
function chatPage() {
  let groups = [];
  let bots = [];
  let templates = [];
  let skills = null;
  let current = '';
  let loads = 0;
  const links = new Map();
  const listMsg = h('div');
  const list = h('div', { class: 'chat-groups' });
  const pane = h('section', { class: 'chat-main' });
  const tgLink = h('a', { class: 'button', href: '#/im', title: '在 Telegram 风格的聊天页打开' }, 'TG 风格界面 ↗');

  const note = (text) => mount(listMsg, h('div', { class: 'notice' }, text));

  const find = (key) => {
    for (const g of groups) {
      const chat = (g.chats || []).find((c) => c.key === key);
      if (chat) return { group: g, chat };
    }
    return null;
  };

  const mark = () => {
    for (const [key, a] of links) a.classList.toggle('active', key === current);
  };

  // reveal scrolls the chat into view when the list is above it.
  const reveal = () => {
    if (matchMedia(NARROW).matches) pane.scrollIntoView();
  };

  const refresh = async () => {
    try {
      await load();
    } catch (err) {
      if (!isAbort(err)) mount(listMsg, errorBox(err));
    }
  };

  const chatMenu = h('div', { class: 'chat-ctx', hidden: true, role: 'menu' });
  let menuKey = '';
  const closeMenu = () => {
    chatMenu.hidden = true;
    menuKey = '';
  };
  const openChatMenu = (e, c) => {
    menuKey = c.key;
    mount(chatMenu, h('button', {
      type: 'button', class: 'chat-ctx-item danger', role: 'menuitem',
      onclick: () => {
        const key = menuKey;
        closeMenu();
        removeChat(key);
      },
    }, '删除聊天'));
    const left = Math.max(0, Math.min(e.clientX, window.innerWidth - 160));
    const top = Math.max(0, Math.min(e.clientY, window.innerHeight - 48));
    chatMenu.style.left = `${left}px`;
    chatMenu.style.top = `${top}px`;
    chatMenu.hidden = false;
  };
  const removeChat = async (key) => {
    mount(listMsg);
    try {
      if (!(await deleteChatConfirmed(key))) return;
      if (current === key) go('#/chat');
      await refresh();
    } catch (err) {
      if (!isAbort(err)) mount(listMsg, errorBox(err));
    }
  };
  list.addEventListener('contextmenu', (e) => {
    let key = '';
    for (const [k, a] of links) {
      if (a === e.target || a.contains(e.target)) {
        key = k;
        break;
      }
    }
    const shown = key ? find(key) : null;
    if (!shown) return;
    e.preventDefault();
    openChatMenu(e, shown.chat);
  });
  document.addEventListener('click', closeMenu);
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeMenu();
  });
  document.addEventListener('scroll', closeMenu, true);
  window.addEventListener('resize', closeMenu);
  window.addEventListener('hashchange', closeMenu);

  const chatDialog = newChatDialog((key) => {
    refresh().then(() => go(href('chat', key)));
  });

  const botForm = botDialog((bot, isNew) => {
    note(isNew ? `已新建 Bot：${bot.name}` : `已保存 Bot：${bot.name}`);
    refresh();
  });

  const configForm = configBotDialog((bot) => {
    note(`已保存 Bot：${bot.name}`);
    refresh();
  });

  // After a delete that stopped partway, the chat on show may be gone too:
  // the list read afresh tells.
  const deleteForm = deleteBotDialog(async (bot, ok) => {
    if (ok) note(`已删除 Bot：${bot.name}`);
    const shown = find(current);
    const onBot = shown && shown.group.id === bot.id;
    if (ok && onBot) go('#/chat');
    await refresh();
    if (!ok && onBot && current === shown.chat.key && !find(current)) go('#/chat');
  });

  // loadSkills reads the skills a panel bot and those a config bot may
  // enable, once.
  const loadSkills = () => {
    if (!skills) {
      skills = get('bots/skills');
      skills.catch(() => {
        skills = null;
      });
    }
    return skills;
  };

  // editBot opens the form of bot, a new panel bot when null: a config bot's
  // or a panel bot's.
  const editBot = async (bot) => {
    mount(listMsg);
    try {
      const res = await loadSkills();
      if (bot && bot.source !== PANEL) {
        configForm.open(bot, { templates, skills: res.config_skills || [], bots });
      } else {
        botForm.open(bot, { templates, skills: res.skills || [], bots });
      }
    } catch (err) {
      if (!isAbort(err)) mount(listMsg, errorBox(err));
    }
  };

  const deleteButton = (bot) => h('button', {
    type: 'button',
    class: 'link danger',
    onclick: () => {
      mount(listMsg);
      deleteForm.open(bot);
    },
  }, '删除');

  const chatItem = (c) => {
    const a = h('a', {
      href: href('chat', c.key),
      title: c.last_message_at ? `最后消息 ${fmtTime(c.last_message_at)}` : '还没有消息',
    },
      h('span', { class: 'chat-title' }, c.title || c.key),
      h('span', { class: 'chat-time muted' }, fmtClock(c.last_message_at)));
    links.set(c.key, a);
    return h('li', null, a);
  };

  const group = (g) => {
    const bot = bots.find((b) => b.id === g.id);
    const panelBot = g.source === PANEL;
    const chats = g.chats || [];
    return h('section', { class: 'chat-group' },
      h('div', { class: 'chat-group-head' },
        h('strong', null, g.name || g.id),
        panelBot ? null : h('span', { class: 'badge' }, '配置'),
        h('span', { class: 'spacer' }),
        bot ? [
          h('button', { type: 'button', class: 'link', onclick: () => editBot(bot) }, '编辑'),
          bot.source === PANEL ? deleteButton(bot) : null,
        ] : null),
      ((bot && bot.problems) || []).map((p) => h('p', { class: 'hint warn' }, p)),
      chats.length ? h('ul', { class: 'chat-items' }, chats.map(chatItem)) : h('p', { class: 'muted small' }, '还没有聊天'),
      h('button', { type: 'button', onclick: () => chatDialog.open(groups, g.id) }, '新建聊天'));
  };

  // load reads the bots and their chats and shows them, unless a later load
  // started meanwhile.
  async function load(signal) {
    const n = ++loads;
    const [chats, all] = await Promise.all([get('chats', null, signal), get('bots', null, signal)]);
    if (n !== loads) return;
    groups = chats.bots || [];
    bots = all.bots || [];
    templates = all.templates || [];
    links.clear();
    mount(list, groups.map(group));
    mark();
  }

  async function select(key, signal) {
    current = key;
    tgLink.href = key ? href('im', key) : '#/im';
    mark();
    if (!key) {
      mount(pane, h('div', { class: 'chat-empty' },
        h('p', null, '从聊天列表里选一个聊天，或者在某个 bot 下点“新建聊天”。')));
      return;
    }
    const shown = find(key);
    const headWithDelete = (el) => {
      if (el) {
        el.appendChild(h('button', {
          type: 'button', class: 'link danger',
          onclick: () => removeChat(key),
        }, '删除'));
      }
      return el;
    };
    let head = shown ? headWithDelete(chatHead(key, shown.chat.title || key, shown.group.name || shown.group.id)) : null;
    mount(pane, head, loading());
    reveal();
    try {
      if (!shown) {
        const conv = await get(convPath(key), null, signal);
        if (conv.platform !== PANEL) {
          mount(pane, errorBox({ message: '聊天页只能打开面板里新建的聊天。' }),
            h('p', null, h('a', { href: href('conversations', key) }, '打开会话详情')));
          return;
        }
        head = headWithDelete(chatHead(key, conv.platform_conversation_id, conv.bot_name || conv.bot_instance_id));
      }
      const nodes = await chatPane({ key, signal, hint: PANEL_HINT });
      if (!signal.aborted) {
        mount(pane, head, nodes);
        reveal();
      }
    } catch (err) {
      if (!isAbort(err) && !signal.aborted) mount(pane, errorBox(err));
    }
  }

  const layout = h('div', { class: 'chat-classic' },
    h('div', { class: 'chat-classic-title' },
      h('h1', null, '聊天'),
      tgLink),
    h('div', { class: 'chat-page' },
      h('aside', { class: 'chat-side' },
        h('div', { class: 'toolbar' },
          h('button', {
            type: 'button',
            class: 'primary',
            onclick: () => editBot(null),
          }, '新建 Bot'),
          h('span', { class: 'spacer' }),
          h('button', {
            type: 'button',
            class: 'link',
            title: '左栏不会自动更新，点这里重新读取',
            onclick: () => {
              mount(listMsg);
              refresh();
            },
          }, '刷新')),
        listMsg,
        list),
      pane),
    chatMenu, chatDialog.dialog, botForm.dialog, configForm.dialog, deleteForm.dialog);

  return { layout, load, select };
}

// page is the chat page on screen. Switching chats reuses it, rebuilding only
// the right pane; leaving the page detaches its layout, so the next visit
// reads the list afresh.
let page = null;

export async function chatsClassicView(ctx) {
  if (!page || page.layout.parentNode !== ctx.el) {
    const next = chatPage();
    await next.load(ctx.signal);
    if (ctx.signal.aborted) return;
    page = next;
    ctx.show(page.layout);
  }
  page.select(ctx.params.key || '', ctx.signal);
}
