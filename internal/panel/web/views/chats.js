import { h, s, mount, errorBox, loading, field, kv } from '../lib/dom.js';
import { get, post, put, request, convPath, isAbort, currentUser, logout } from '../lib/api.js';
import { go, href, withQuery } from '../lib/router.js';
import { fmtTime, fmtClock } from '../lib/format.js';
import { SECTIONS } from '../lib/sections.js';
import { chatBar, chatPane, deleteChatConfirmed } from './chat.js';
import { avatarEl } from './avatar.js';

// PANEL is the source of a bot created in the panel and the platform of the
// chats held in the panel.
const PANEL = 'panel';

// NARROW is the media query under which style.css shows the chat page in one
// column, like Telegram on a phone: the folders, in a row, and the chat list
// while no chat is open, then the chat alone, with ← back to the list. The
// page marks its layout open while a chat is.
const NARROW = '(max-width: 699px)';

// MAX_DISPLAY_NAME is the longest name a panel chat calls its user by.
const MAX_DISPLAY_NAME = 32;

const PANEL_HINT = '在面板里和 bot 私聊，你是这个聊天的管理员。bot 的回复只出现在面板里。每条最多 2000 字。';

// PRIVATE_CHAT names the private ranges of a config bot by the values of
// private_chat, from the most closed to the most open.
const PRIVATE_CHAT = {
  off: '关闭：不回复任何私聊',
  admins: '只限管理员',
  allowlist: '管理员和私聊白名单里的人',
  all: '所有人',
};

// botLabel names a bot among bots to choose from: a panel bot as 名字（面板）,
// a config bot by its name and ID.
function botLabel(b) {
  if (b.source === PANEL) return `${b.name}（面板）`;
  return b.name ? `${b.name}（${b.id}）` : b.id;
}

// newChatDialog asks for a bot and the name the bot calls you, starts a panel
// chat and hands its key to created. open(bots, botId) shows it with bots to
// choose from, botId chosen when given.
export function newChatDialog(created) {
  const bot = h('select', { required: true });
  const name = h('input', { maxlength: MAX_DISPLAY_NAME, placeholder: '留空时用面板用户名' });
  const msg = h('div');
  const create = h('button', { type: 'submit', class: 'primary' }, '开始聊天');
  const dialog = h('dialog', { class: 'card dialog' });
  const cancel = h('button', { type: 'button', onclick: () => dialog.close() }, '取消');
  mount(dialog, h('form', {
    onsubmit: async (e) => {
      e.preventDefault();
      create.disabled = true;
      mount(msg);
      try {
        const res = await post('chats', { bot_instance_id: bot.value, display_name: name.value.trim() });
        dialog.close();
        name.value = '';
        created(res.key);
      } catch (err) {
        if (!isAbort(err)) mount(msg, errorBox(err));
      } finally {
        create.disabled = false;
      }
    },
  },
  h('h2', null, '新建聊天'),
  h('p', { class: 'hint' }, '在面板里和 bot 私聊。你是这个聊天的管理员，私聊白名单对它不生效。'),
  field('Bot', bot),
  field('称呼', name, `bot 怎么称呼你，最多 ${MAX_DISPLAY_NAME} 字`),
  msg,
  h('div', { class: 'toolbar' }, create, cancel)));
  const open = (bots, botId) => {
    mount(bot, bots.map((b) => h('option', { value: b.id, selected: b.id === botId }, botLabel(b))));
    mount(msg);
    dialog.showModal();
  };
  return { dialog, open };
}

// nameInput is the name field of a bot's form, with a note that points out,
// without refusing it, a name another of bots (all the bots) already has;
// bot is the bot edited, null for a new one.
function nameInput(bot, bots) {
  const input = h('input', { required: true, value: bot ? bot.name : '' });
  const same = h('p', { class: 'hint warn', hidden: true });
  const check = () => {
    const v = input.value.trim();
    const others = bots.filter((b) => (!bot || b.id !== bot.id) && b.name === v);
    same.hidden = !v || others.length === 0;
    same.textContent = `已有同名的 bot：${others.map(botLabel).join('、')}。可以重名，只是不好区分。`;
  };
  input.addEventListener('input', check);
  check();
  return { input, same };
}

// templateSelect chooses a persona template among templates, current chosen
// and listed first when it is not among them.
function templateSelect(templates, current) {
  const names = templates.includes(current) ? templates : [current, ...templates];
  return h('select', null, names.map((t) => h('option', { value: t, selected: t === current }, t)));
}

// skillBoxes are a checkbox for each of skills, checked as checked(name)
// says; note(skill), when given, adds a hint to its label.
function skillBoxes(skills, checked, note) {
  return skills.map((sk) => ({
    name: sk.name,
    box: h('input', { type: 'checkbox', checked: checked(sk.name) }),
    description: sk.description,
    note: note ? note(sk) : '',
  }));
}

// checkedSkills names the skills whose box is checked.
function checkedSkills(boxes) {
  return boxes.filter((b) => b.box.checked).map((b) => b.name);
}

// skillsFieldset lists boxes under 技能, or says none when there are none.
function skillsFieldset(boxes, none) {
  return h('fieldset', { class: 'skills' },
    h('legend', null, '技能'),
    boxes.length
      ? boxes.map((b) => h('label', { class: 'skill' }, b.box,
        h('span', null, h('strong', null, b.name),
          b.description ? h('span', { class: 'hint' }, b.description) : null,
          b.note ? h('span', { class: 'hint' }, b.note) : null)))
      : h('p', { class: 'muted' }, none));
}

// botDialog creates a panel bot or edits one: its name, the persona template
// its new chats start from and its skills. saved(bot, isNew) runs after a
// save.
function botDialog(saved) {
  const dialog = h('dialog', { class: 'card dialog wide' });
  // open shows the form for bot, or for a new bot when bot is null, with the
  // templates and skills to choose from. bots are all the bots: a name one of
  // them already has is pointed out, not refused.
  const open = (bot, { templates, skills, bots }) => {
    const name = nameInput(bot, bots);
    const template = templateSelect(templates, bot ? bot.persona_template : 'default');
    const boxes = skillBoxes(skills, (s) => (bot ? (bot.skills || []).includes(s) : true));
    const msg = h('div');
    const save = h('button', { type: 'submit', class: 'primary' }, bot ? '保存' : '新建');
    const cancel = h('button', { type: 'button', onclick: () => dialog.close() }, '取消');
    mount(dialog, h('form', {
      onsubmit: async (e) => {
        e.preventDefault();
        save.disabled = true;
        mount(msg);
        const body = {
          name: name.input.value.trim(),
          persona_template: template.value,
          skills: checkedSkills(boxes),
        };
        try {
          const res = bot ? await put(`bots/${encodeURIComponent(bot.id)}`, body) : await post('bots', body);
          dialog.close();
          saved(res, !bot);
        } catch (err) {
          if (!isAbort(err)) mount(msg, errorBox(err));
        } finally {
          save.disabled = false;
        }
      },
    },
    h('h2', null, bot ? `编辑 Bot：${bot.name}` : '新建 Bot'),
    bot
      ? h('p', { class: 'hint mono' }, bot.id)
      : h('p', { class: 'hint' }, '面板 bot 只在面板里聊天，不接 QQ、Telegram 等平台。建好立即生效，不用重启。'),
    field('名字', name.input, '1–32 字，不能有换行和不可见字符'),
    name.same,
    field('默认人设模板', template, bot
      ? '只影响之后新建的聊天，以及 bot 还没回复过的聊天；已有聊天的人设在会话的“人设”页里改'
      : '新建的聊天从这个模板开始'),
    skillsFieldset(boxes, '没有可以在私聊里用的技能'),
    h('p', { class: 'hint' }, bot ? '改名字和技能从下一次回复起生效。' : '技能默认全选，可以一个都不选。'),
    msg,
    h('div', { class: 'toolbar' }, save, cancel)));
    dialog.showModal();
  };
  return { dialog, open };
}

// idList is a textarea of platform IDs, one a line; ids reads them back,
// without blank lines and the spaces around each.
function idList(list, placeholder) {
  return h('textarea', { rows: 3, placeholder, value: (list || []).join('\n') });
}

function ids(textarea) {
  return textarea.value.split('\n').map((s) => s.trim()).filter(Boolean);
}

// conversationsNote says which conversations a skill applies to when it is
// not both kinds.
function conversationsNote(sk) {
  const convs = sk.conversations || [];
  if (!convs.includes('private')) return '只在群聊里用';
  if (!convs.includes('group')) return '只在私聊里用';
  return '';
}

// configBotDialog edits the settings of a config bot (QQ, Telegram): its
// name, admins, private range and allowlist, groups, the persona templates of
// its new private chats and new groups and its skills. Its identity is only
// shown: it is changed in the config file. saved(bot) runs after a save.
function configBotDialog(saved) {
  const dialog = h('dialog', { class: 'card dialog wide' });
  // open shows the form for bot with the templates and skills to choose
  // from; bots are all the bots, as for botDialog.
  const open = (bot, { templates, skills, bots }) => {
    const name = nameInput(bot, bots);
    const admins = idList(bot.admins, '每行一个平台用户 ID');
    const privateChat = h('select', null, Object.keys(PRIVATE_CHAT).map((k) =>
      h('option', { value: k, selected: k === bot.private_chat }, PRIVATE_CHAT[k])));
    const allowlist = idList(bot.private_allowlist, '每行一个平台用户 ID');
    const unused = h('p', { class: 'hint warn', hidden: true }, '私聊开放范围不是“管理员和私聊白名单里的人”，白名单现在不生效。');
    const checkAllowlist = () => {
      unused.hidden = privateChat.value === 'allowlist' || ids(allowlist).length === 0;
    };
    privateChat.addEventListener('change', checkAllowlist);
    allowlist.addEventListener('input', checkAllowlist);
    checkAllowlist();
    const groups = idList(bot.groups, '每行一个群 ID');
    const privateTemplate = templateSelect(templates, bot.private_template);
    const groupTemplate = templateSelect(templates, bot.group_template);
    const boxes = skillBoxes(skills, (s) => (bot.skills || []).includes(s), conversationsNote);
    const msg = h('div');
    const save = h('button', { type: 'submit', class: 'primary' }, '保存');
    const cancel = h('button', { type: 'button', onclick: () => dialog.close() }, '取消');
    mount(dialog, h('form', {
      onsubmit: async (e) => {
        e.preventDefault();
        save.disabled = true;
        mount(msg);
        const body = {
          name: name.input.value.trim(),
          admins: ids(admins),
          private_chat: privateChat.value,
          private_allowlist: ids(allowlist),
          groups: ids(groups),
          private_template: privateTemplate.value,
          group_template: groupTemplate.value,
          skills: checkedSkills(boxes),
        };
        try {
          const res = await put(`bots/${encodeURIComponent(bot.id)}`, body);
          dialog.close();
          saved(res);
        } catch (err) {
          if (!isAbort(err)) mount(msg, errorBox(err));
        } finally {
          save.disabled = false;
        }
      },
    },
    h('h2', null, `编辑 Bot：${bot.name}`),
    kv([['ID', bot.id], ['平台', bot.platform], ['平台自身 ID', bot.platform_self_id]]),
    h('p', { class: 'hint' }, 'ID、平台和平台自身 ID 不能在这里改，在配置文件里修改'),
    field('显示名', name.input, '1–32 字，不能有换行和不可见字符'),
    name.same,
    field('管理员', admins, '每行一个平台用户 ID，可以用 /pause 等管理员命令'),
    field('私聊开放范围', privateChat),
    field('私聊白名单', allowlist, '每行一个平台用户 ID，只在开放范围是“管理员和私聊白名单里的人”时生效'),
    unused,
    field('启用的群', groups, '每行一个群 ID；没列出的群里的消息会被忽略'),
    field('私聊默认模板', privateTemplate, '新私聊第一次回复时复制这个模板；已有聊天的人设在会话的“人设”页里改'),
    field('群聊默认模板', groupTemplate, '新群第一次回复时复制这个模板'),
    skillsFieldset(boxes, '没有可以启用的技能'),
    h('p', { class: 'hint' }, 'ID 不能有空白和冒号。保存后 core 立即按新设置工作；适配器每 5 分钟读取一次启用的群和私聊开放范围，最多 5 分钟后跟上。'),
    msg,
    h('div', { class: 'toolbar' }, save, cancel)));
    dialog.showModal();
  };
  return { dialog, open };
}

// deleteBotDialog deletes a panel bot once its name is typed in. done(bot, ok)
// runs after the delete, ok false when it stopped partway and some of the
// bot's chats may already be gone.
function deleteBotDialog(done) {
  const dialog = h('dialog', { class: 'card dialog' });
  const open = (bot) => {
    const confirmName = h('input', { autocomplete: 'off' });
    const msg = h('div');
    const remove = h('button', { type: 'submit', class: 'danger', disabled: true }, '删除');
    const cancel = h('button', { type: 'button', onclick: () => dialog.close() }, '取消');
    const matches = () => confirmName.value.trim() === bot.name;
    confirmName.addEventListener('input', () => {
      remove.disabled = !matches();
    });
    mount(dialog, h('form', {
      onsubmit: async (e) => {
        e.preventDefault();
        if (!matches()) return;
        remove.disabled = true;
        cancel.disabled = true;
        remove.textContent = '删除中…';
        mount(msg);
        try {
          await request('DELETE', `bots/${encodeURIComponent(bot.id)}`, { body: { confirm_name: confirmName.value.trim() } });
          dialog.close();
          done(bot, true);
        } catch (err) {
          if (isAbort(err)) return;
          mount(msg, errorBox(err.status === 409
            ? { message: `没有删完，稍后再试一次：有聊天正在生成回复、没能及时停下，或者这个 bot 正在删除中。已经删掉的聊天不会恢复。（${err.message}）` }
            : err));
          if (err.status === 409) done(bot, false);
        } finally {
          remove.textContent = '删除';
          remove.disabled = !matches();
          cancel.disabled = false;
        }
      },
    },
    h('h2', null, `删除 Bot：${bot.name}`),
    h('p', null, `会删除它名下的 ${bot.chats} 个聊天和这些聊天的全部数据（消息、记忆、人设、状态、定时等），然后删除这个 bot，不能恢复。模型用量统计和审计记录保留。`),
    field(`输入 bot 的名字“${bot.name}”确认`, confirmName),
    msg,
    h('div', { class: 'toolbar' }, remove, cancel)));
    dialog.showModal();
  };
  return { dialog, open };
}

// SEEN_KEY is where the browser keeps, as JSON { [key]: count }, how many
// messages each chat had when it was last seen; the rest are unread.
const SEEN_KEY = 'conloom.chat.seen';

// REFRESH_MS is how often the chat page reads the chat list again.
const REFRESH_MS = 5000;

// BOTS_EVERY is how many of those reads make one that also reads the bots, so
// that bot settings changed elsewhere show within BOTS_EVERY × REFRESH_MS.
const BOTS_EVERY = 6;

function readSeen() {
  try {
    const v = JSON.parse(localStorage.getItem(SEEN_KEY) || '{}');
    return v && typeof v === 'object' && !Array.isArray(v) ? v : {};
  } catch {
    return {};
  }
}

function saveSeen(seen) {
  try {
    localStorage.setItem(SEEN_KEY, JSON.stringify(seen));
  } catch {
    // Without storage every chat counts as read.
  }
}

// noteSeen updates the counts of seen messages for chats, the chats listed,
// and returns them. The chat open, current, is seen up to its latest message,
// and so is a chat seen for the first time, so that a new browser does not
// start with every chat unread, or one with fewer messages than counted, some
// of them deleted. Chats no longer listed are forgotten.
function noteSeen(chats, current) {
  const old = readSeen();
  const seen = {};
  for (const c of chats) {
    const n = c.message_count || 0;
    const s = Number(old[c.key]);
    seen[c.key] = c.key === current || !Number.isFinite(s) || s > n ? n : s;
  }
  saveSeen(seen);
  return seen;
}

// unreadBadge is the round count of unread messages, none when there are
// none.
function unreadBadge(n, cls) {
  if (n <= 0) return null;
  return h('span', { class: cls }, n > 99 ? '99+' : String(n));
}

// PREVIEW_RUNES is how many characters of the last message GET chats
// previews, as maxChatLastRunes.
const PREVIEW_RUNES = 60;

// lastOf is message m, as GET chat returns it, in the form of a chat's last
// in GET chats: on one line and cut to PREVIEW_RUNES characters, with …
// when cut.
function lastOf(m) {
  const text = (m.text || '').split(/\s+/).filter(Boolean).join(' ');
  const runes = [...text];
  return {
    seq: m.seq,
    from_bot: !!m.from_bot,
    sender_id: m.sender_id || '',
    at: m.at,
    text: runes.length > PREVIEW_RUNES ? runes.slice(0, PREVIEW_RUNES).join('') + '…' : text,
  };
}

// panelActorID is the sender ID of the panel user user's messages in panel
// chats, as core.PanelActorID.
function panelActorID(user) {
  return 'panel_' + String(user || '').replace(/:/g, '_');
}

// recency orders chats by their last message, newest first; a chat without
// messages, as one just started, comes first.
function recency(c) {
  if (!c.last_message_at) return Infinity;
  const t = Date.parse(c.last_message_at);
  return Number.isNaN(t) ? 0 : t;
}

// allIcon is the icon of the 全部 folder: two speech bubbles.
function allIcon() {
  return s('svg', { class: 'tg-folder-icon', viewBox: '0 0 24 24', 'aria-hidden': 'true' },
    s('path', { d: 'M4 4h12a2 2 0 0 1 2 2v7a2 2 0 0 1-2 2H9l-4 3v-3H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2z' }),
    s('path', { d: 'M20 8a2 2 0 0 1 2 2v7a2 2 0 0 1-2 2h-1v3l-4-3h-4' }));
}

// chatPage lays out the chat page like Telegram Desktop: a rail of folders
// (≡, 全部 and a folder per bot), the chat list of the folder chosen, and the
// chat open. select(key, signal) shows the chat key, or a pill when key is
// empty; setFolder(id) chooses the folder of bot id, all chats when empty;
// watch(signal) reads the list, at once when it has not been read yet,
// then again every REFRESH_MS while the page is visible until signal fires.
// Reading the list again replaces the folders and the list, only when they
// changed, but leaves the chat as it is.
function chatPage() {
  // fetched is the bots of the last GET chats shown, groups the same with
  // the messages arrived counted in.
  let fetched = null;
  let groups = [];
  let bots = [];
  let templates = [];
  let skills = null;
  let current = '';
  let folder = '';
  let seen = {};
  // arrived holds, by chat key, the messages the chat pane showed that the
  // list read last may not count yet: they count as seen at once.
  let arrived = {};
  // pending is the signal of a chat opened before the list was read, which
  // is shown once it is; rendered is the state the folders and the list
  // show (see listState).
  let pending = null;
  let rendered = '';
  // fullLoads counts the full reloads started, fullPending those under way,
  // botReads the reads of the bots started and ticks the polls.
  let fullLoads = 0;
  let fullPending = 0;
  let botReads = 0;
  let ticks = 0;
  const listMsg = h('div');
  const pollMsg = h('div');
  const paneMsg = h('div', { class: 'tg-pane-msg' });
  const folders = h('div', { class: 'tg-folders' });
  const botBar = h('div');
  const list = h('ul', { class: 'tg-chats' });
  const pane = h('section', { class: 'tg-pane' });
  const search = h('input', { type: 'search', placeholder: '搜索', 'aria-label': '搜索', oninput: () => renderList() });

  const note = (text) => mount(listMsg, h('div', { class: 'notice' }, text));

  const find = (key) => {
    for (const g of groups) {
      const chat = (g.chats || []).find((c) => c.key === key);
      if (chat) return { group: g, chat };
    }
    return null;
  };

  // shownFolder is the folder chosen, all chats when its bot is gone.
  const shownFolder = () => (groups.some((g) => g.id === folder) ? folder : '');

  const chatHref = (key) => withQuery(href('chat', key), { bot: folder });
  const folderHref = (id) => withQuery(current ? href('chat', current) : '#/chat', { bot: id });

  const onBack = () => go(withQuery('#/chat', { bot: folder }));

  const unreadOf = (c) => Math.max(0, (c.message_count || 0) - (seen[c.key] === undefined ? c.message_count || 0 : seen[c.key]));

  const allChats = () => groups.flatMap((g) => g.chats || []);

  // choose shows the folder of bot id, all chats when empty, keeping the
  // chat open: the address changes without a new render.
  const choose = (id) => {
    folder = id;
    history.replaceState(null, '', folderHref(id));
    renderLists();
  };

  const refresh = async () => {
    try {
      await load();
    } catch (err) {
      if (!isAbort(err)) mount(listMsg, errorBox(err));
    }
  };

  const chatDialog = newChatDialog((key) => {
    refresh().then(() => {
      const f = find(key);
      go(withQuery(href('chat', key), { bot: folder && f ? f.group.id : folder }));
    });
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
    if (ok && folder === bot.id) choose('');
    if (ok && onBot) onBack();
    await refresh();
    if (!ok && onBot && current === shown.chat.key && !find(current)) onBack();
  });

  // removeChat deletes the chat key once confirmed, then reads the list
  // again and returns to it; it reports whether the chat was deleted.
  const removeChat = async (key) => {
    mount(paneMsg);
    try {
      if (!(await deleteChatConfirmed(key))) return false;
    } catch (err) {
      if (!isAbort(err)) mount(paneMsg, errorBox(err));
      return false;
    }
    await refresh();
    if (current === key) onBack();
    return true;
  };

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

  // readBots reads the bots and the templates, and keeps them unless a later
  // read started meanwhile; it returns what it read.
  async function readBots(signal) {
    const n = ++botReads;
    const all = await get('bots', null, signal);
    if (n === botReads) {
      bots = all.bots || [];
      templates = all.templates || [];
    }
    return all;
  }

  // freshBot reads the bots again and returns the one with bot's ID, with
  // all the bots and the templates, or shows that it is gone.
  const freshBot = async (bot) => {
    const all = await readBots();
    renderBotBar();
    const fresh = (all.bots || []).find((b) => b.id === bot.id);
    if (!fresh) {
      mount(listMsg, errorBox({ message: `Bot “${bot.name}” 已不存在。` }));
      refresh();
    }
    return { fresh, all };
  };

  // editBot opens the form of bot, a new panel bot when null: a config bot's
  // or a panel bot's, with its settings read afresh, so the form never shows
  // settings saved over since.
  const editBot = async (bot) => {
    mount(listMsg);
    try {
      const [res, { fresh, all }] = await Promise.all([loadSkills(), bot ? freshBot(bot) : { fresh: null, all: { bots, templates } }]);
      if (bot && !fresh) return;
      const ctx = { templates: all.templates || [], bots: all.bots || [] };
      if (fresh && fresh.source !== PANEL) {
        configForm.open(fresh, { ...ctx, skills: res.config_skills || [] });
      } else {
        botForm.open(fresh, { ...ctx, skills: res.skills || [] });
      }
    } catch (err) {
      if (!isAbort(err)) mount(listMsg, errorBox(err));
    }
  };

  // deleteButton deletes bot, with its chat count read afresh.
  const deleteButton = (bot) => h('button', {
    type: 'button',
    class: 'link danger',
    onclick: async () => {
      mount(listMsg);
      try {
        const { fresh } = await freshBot(bot);
        if (fresh) deleteForm.open(fresh);
      } catch (err) {
        if (!isAbort(err)) mount(listMsg, errorBox(err));
      }
    },
  }, '删除');

  const signOut = async () => {
    try {
      await logout();
    } catch {
      // the session ends either way
    }
    go('#/login');
  };

  const menuButton = h('button', { type: 'button', class: 'tg-menu-button', title: '菜单', 'aria-label': '菜单' }, '≡');
  const menu = h('div', { class: 'tg-menu', hidden: true },
    h('nav', null, SECTIONS.filter(([section]) => section !== 'chat').map(([section, title]) =>
      h('a', { href: '#/' + section }, title))),
    h('button', {
      type: 'button',
      class: 'tg-menu-item',
      onclick: () => {
        menu.hidden = true;
        editBot(null);
      },
    }, '新建 Bot'),
    h('div', { class: 'tg-menu-user' },
      h('span', { class: 'muted' }, currentUser()),
      h('button', { type: 'button', class: 'link', onclick: signOut }, '退出')));
  menuButton.addEventListener('click', () => {
    menu.hidden = !menu.hidden;
  });

  // warnDot marks a folder whose bot has problems, named in its tooltip.
  const warnDot = (problems) => (problems && problems.length
    ? h('span', { class: 'tg-folder-warn', role: 'img', 'aria-label': '设置有问题', title: problems.join('\n') })
    : null);

  const folderItem = (id, name, icon, unread, problems) => h('a', {
    class: id === shownFolder() ? 'tg-folder active' : 'tg-folder',
    href: folderHref(id),
    title: name,
    onclick: (e) => {
      e.preventDefault();
      choose(id);
    },
  }, h('span', { class: 'tg-folder-pic' }, icon, unreadBadge(unread, 'tg-unread tg-folder-unread'), warnDot(problems)),
  h('span', { class: 'tg-folder-name' }, name));

  const renderFolders = () => {
    const unread = (chats) => (chats || []).reduce((n, c) => n + unreadOf(c), 0);
    const problems = (id) => {
      const bot = bots.find((b) => b.id === id);
      return bot ? bot.problems : null;
    };
    mount(folders,
      folderItem('', '全部', allIcon(), unread(allChats())),
      groups.map((g) => folderItem(g.id, g.name || g.id, avatarEl(g, 36), unread(g.chats), problems(g.id))));
  };

  // renderBotBar names the bot of the folder chosen, with 编辑 and, for a
  // panel bot, 删除, and what is wrong with its settings.
  const renderBotBar = () => {
    const g = groups.find((x) => x.id === shownFolder());
    const bot = g ? bots.find((b) => b.id === g.id) : null;
    mount(botBar, g ? h('div', { class: 'tg-botbar' },
      h('div', { class: 'tg-botbar-head' },
        h('strong', null, g.name || g.id),
        g.source === PANEL ? null : h('span', { class: 'badge' }, '配置'),
        h('span', { class: 'spacer' }),
        bot ? [
          h('button', { type: 'button', class: 'link', onclick: () => editBot(bot) }, '编辑'),
          bot.source === PANEL ? deleteButton(bot) : null,
        ] : null),
      ((bot && bot.problems) || []).map((p) => h('p', { class: 'hint warn' }, p))) : null);
  };

  // preview is the last message of chat c on one line, after 你： when you,
  // the panel user signed in, sent it.
  const preview = (c) => {
    if (!c.last) return h('span', { class: 'tg-chat-preview muted' }, '还没有消息');
    const mine = !c.last.from_bot && c.last.sender_id === panelActorID(currentUser());
    return h('span', { class: 'tg-chat-preview' },
      mine ? h('span', { class: 'tg-you' }, '你：') : null, c.last.text);
  };

  // chatRow is chat c of bot g; with several chats of g it also names who
  // the bot talks with.
  const chatRow = (g, c, several) => h('li', null, h('a', {
    class: c.key === current ? 'tg-chat active' : 'tg-chat',
    href: chatHref(c.key),
    title: c.last_message_at ? `最后消息 ${fmtTime(c.last_message_at)}` : '还没有消息',
  },
  avatarEl(g, 48),
  h('span', { class: 'tg-chat-body' },
    h('span', { class: 'tg-chat-line' },
      h('strong', { class: 'tg-chat-name' }, g.name || g.id),
      several ? h('span', { class: 'tg-chat-title' }, c.title || c.key) : null,
      h('span', { class: 'tg-chat-time' }, fmtClock(c.last_message_at))),
    h('span', { class: 'tg-chat-line' },
      preview(c),
      unreadBadge(unreadOf(c), 'tg-unread')))));

  // renderList lists the chats of the folder chosen that match the search,
  // by bot name, who the bot talks with or the last message.
  const renderList = () => {
    const id = shownFolder();
    const q = search.value.trim().toLowerCase();
    const rows = [];
    for (const g of groups) {
      if (id && g.id !== id) continue;
      const chats = g.chats || [];
      for (const c of chats) rows.push({ g, c, several: chats.length > 1 });
    }
    const shown = rows.filter(({ g, c }) => !q ||
      [g.name || g.id, c.title, c.last ? c.last.text : ''].some((t) => (t || '').toLowerCase().includes(q)));
    shown.sort((a, b) => recency(b.c) - recency(a.c));
    if (!fetched) {
      mount(list, h('li', { class: 'tg-list-empty muted' }, '加载中…'));
    } else if (!rows.length) {
      mount(list, h('li', { class: 'tg-list-empty muted' }, '还没有聊天，点右下角的 ✎ 开始'));
    } else if (!shown.length) {
      mount(list, h('li', { class: 'tg-list-empty muted' }, `没有符合“${search.value.trim()}”的聊天`));
    } else {
      mount(list, shown.map(({ g, c, several }) => chatRow(g, c, several)));
    }
  };

  // listState is what the folders and the list show, but the search, as a
  // string; the day is in it for the times of the last messages.
  const listState = () => JSON.stringify([groups, seen, bots, shownFolder(), current, new Date().toDateString()]);

  function renderLists() {
    rendered = listState();
    renderFolders();
    renderBotBar();
    renderList();
  }

  // renderChanged renders the folders and the list again only when what
  // they show changed, so that a poll that reads the same list keeps the
  // focus, a click under way and the avatars already loaded or failed.
  const renderChanged = () => {
    if (listState() !== rendered) renderLists();
  };

  // withArrived is chat c counting the messages arrived after its last.
  const withArrived = (c) => {
    const rows = (arrived[c.key] || []).filter((m) => !c.last || m.seq > c.last.seq);
    if (!rows.length) return c;
    const m = rows[rows.length - 1];
    const after = !c.last_message_at || Date.parse(m.at) > Date.parse(c.last_message_at);
    return {
      ...c,
      message_count: (c.message_count || 0) + rows.length,
      last: m,
      last_message_at: after ? m.at : c.last_message_at,
    };
  };

  // showFetched shows the list read last with the messages arrived since,
  // the chat open seen up to its newest.
  const showFetched = () => {
    if (!fetched) return;
    groups = fetched.map((g) => ({ ...g, chats: (g.chats || []).map(withArrived) }));
    seen = noteSeen(allChats(), current);
  };

  // showChats shows chats, the response of GET chats, and forgets the
  // messages arrived that it counts, and those of chats it no longer lists
  // but the chat open, which may be too new for it.
  const showChats = (chats) => {
    fetched = chats.bots || [];
    const listed = {};
    for (const g of fetched) for (const c of g.chats || []) listed[c.key] = c;
    const kept = {};
    for (const [key, rows] of Object.entries(arrived)) {
      const c = listed[key];
      const left = c ? rows.filter((m) => !c.last || m.seq > c.last.seq) : key === current ? rows : [];
      if (left.length) kept[key] = left;
    }
    arrived = kept;
    showFetched();
  };

  // noteArrived counts messages, just shown in the chat key, as arrived and
  // seen while key is still open.
  const noteArrived = (key, messages) => {
    if (key !== current) return;
    const known = new Set((arrived[key] || []).map((m) => m.seq));
    arrived[key] = (arrived[key] || []).concat(messages.filter((m) => !known.has(m.seq)).map(lastOf));
    showFetched();
    renderChanged();
  };

  // load reads the list again: a full reload reads the chats and the bots,
  // a light one (withBots false), as the polls do, only the chats, and the
  // bots when a bot is new to the page. A full reload shows its chats unless
  // a later full reload started meanwhile; a light one never supersedes a
  // full one: it reads nothing while one is under way, and drops its chats
  // when one started meanwhile. The bots are shown as readBots says.
  async function load(signal, withBots = true) {
    if (withBots) {
      const n = ++fullLoads;
      fullPending++;
      try {
        const [chats] = await Promise.all([get('chats', null, signal), readBots(signal)]);
        if (n === fullLoads) showChats(chats);
      } finally {
        fullPending--;
        renderChanged();
      }
      return;
    }
    if (fullPending) return;
    const n = fullLoads;
    const chats = await get('chats', null, signal);
    if ((chats.bots || []).some((g) => !bots.some((b) => b.id === g.id))) await readBots(signal);
    if (fullPending || n !== fullLoads) return;
    showChats(chats);
    renderChanged();
  }

  // watch reads the list, the first time with the bots, and shows a chat
  // opened before it was read. It pauses while the page is hidden and reads
  // the list at once when the page shows again. A failed read is shown in
  // pollMsg until one succeeds.
  function watch(signal) {
    let timer = 0;
    let running = false;
    const tick = async () => {
      clearTimeout(timer);
      if (signal.aborted || running || document.hidden) return;
      running = true;
      try {
        await load(signal, !fetched || ++ticks % BOTS_EVERY === 0);
        mount(pollMsg);
      } catch (err) {
        if (!isAbort(err) && !signal.aborted) mount(pollMsg, errorBox(err));
      } finally {
        running = false;
      }
      if (signal.aborted) return;
      if (fetched && pending === signal) select(current, signal);
      if (!document.hidden) timer = setTimeout(tick, REFRESH_MS);
    };
    const onVisibility = () => {
      if (!document.hidden) tick();
    };
    document.addEventListener('visibilitychange', onVisibility);
    signal.addEventListener('abort', () => {
      clearTimeout(timer);
      document.removeEventListener('visibilitychange', onVisibility);
    });
    timer = setTimeout(tick, fetched ? REFRESH_MS : 0);
  }

  function setFolder(id) {
    folder = id;
  }

  async function select(key, signal) {
    current = key;
    pending = null;
    layout.classList.toggle('open', !!key);
    if (key && fetched) seen = noteSeen(allChats(), key);
    renderLists();
    mount(paneMsg);
    if (!key) {
      mount(pane, h('div', { class: 'tg-empty' }, h('span', { class: 'tg-pill' }, '选择一个对话开始聊天')));
      return;
    }
    const shown = find(key);
    let bot = shown ? { id: shown.group.id, name: shown.group.name || shown.group.id, avatar: shown.group.avatar || '' } : null;
    let title = shown ? shown.chat.title || key : '';
    const onDelete = () => removeChat(key);
    const onMessages = (messages) => {
      if (!signal.aborted) noteArrived(key, messages);
    };
    const bar = (b, t) => chatBar({ key, bot: b, title: t, onBack, signal });
    mount(pane, bar(bot, title), loading());
    if (!fetched) {
      pending = signal;
      return;
    }
    try {
      if (!shown) {
        const conv = await get(convPath(key), null, signal);
        if (conv.platform !== PANEL) {
          mount(pane, bar(null, conv.platform_conversation_id),
            errorBox({ message: '聊天页只能打开面板里新建的聊天。' }),
            h('p', null, h('a', { href: href('conversations', key) }, '打开会话详情')));
          return;
        }
        const g = groups.find((x) => x.id === conv.bot_instance_id);
        bot = { id: conv.bot_instance_id, name: conv.bot_name || conv.bot_instance_id, avatar: g ? g.avatar || '' : '' };
        title = conv.platform_conversation_id;
      }
      const [head, ...body] = await chatPane({ key, signal, hint: PANEL_HINT, bot, title, onBack, onDelete, onMessages });
      if (!signal.aborted) mount(pane, head, paneMsg, body);
    } catch (err) {
      if (!isAbort(err) && !signal.aborted) mount(pane, bar(bot, title), errorBox(err));
    }
  }

  const layout = h('div', { class: 'tg' },
    h('nav', { class: 'tg-rail' }, menuButton, folders),
    menu,
    h('section', { class: 'tg-list' },
      h('div', { class: 'tg-search' }, search),
      botBar,
      listMsg,
      pollMsg,
      list,
      h('button', {
        type: 'button',
        class: 'tg-fab',
        title: '新建聊天',
        'aria-label': '新建聊天',
        onclick: () => {
          mount(listMsg);
          chatDialog.open(groups, shownFolder());
        },
      }, '✎')),
    pane,
    chatDialog.dialog, botForm.dialog, configForm.dialog, deleteForm.dialog);

  // A click outside the ≡ menu, or Escape, closes it.
  layout.addEventListener('click', (e) => {
    if (!menu.hidden && !menu.contains(e.target) && !menuButton.contains(e.target)) menu.hidden = true;
  });
  layout.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !menu.hidden) {
      menu.hidden = true;
      menuButton.focus();
    }
  });

  return { layout, select, setFolder, watch };
}

// page is the chat page on screen. Switching chats reuses it, rebuilding only
// the chat pane; leaving the page detaches its layout, so the next visit
// reads the list afresh. The page shows before the list is read, so that its
// ≡ menu is there even when reading the list fails; watch reads it, and
// keeps trying.
let page = null;

export function chatsView(ctx) {
  if (!page || page.layout.parentNode !== ctx.el) {
    page = chatPage();
    ctx.show(page.layout);
  }
  page.setFolder(ctx.query.get('bot') || '');
  page.select(ctx.params.key || '', ctx.signal);
  page.watch(ctx.signal);
}
