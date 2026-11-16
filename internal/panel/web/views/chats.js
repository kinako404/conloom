import { h, mount, errorBox, loading, field, kv } from '../lib/dom.js';
import { get, post, put, request, convPath, isAbort } from '../lib/api.js';
import { go, href } from '../lib/router.js';
import { fmtTime, fmtClock } from '../lib/format.js';
import { chatPane } from './chat.js';

// PANEL is the source of a bot created in the panel and the platform of the
// chats held in the panel.
const PANEL = 'panel';

// NARROW is the media query under which style.css puts the chat list above
// the chat.
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
// the chat. load reads the list; select(key, signal) shows the chat key,
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
    const a = h('a', { href: href('chat', c.key), title: c.last_message_at ? `最后消息 ${fmtTime(c.last_message_at)}` : '还没有消息' },
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
    mark();
    if (!key) {
      mount(pane, h('div', { class: 'chat-empty' },
        h('p', null, '从聊天列表里选一个聊天，或者在某个 bot 下点“新建聊天”。')));
      return;
    }
    const shown = find(key);
    let head = shown ? chatHead(key, shown.chat.title || key, shown.group.name || shown.group.id) : null;
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
        head = chatHead(key, conv.platform_conversation_id, conv.bot_name || conv.bot_instance_id);
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

  const layout = h('div', null,
    h('h1', null, '聊天'),
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
    chatDialog.dialog, botForm.dialog, configForm.dialog, deleteForm.dialog);

  return { layout, load, select };
}

// page is the chat page on screen. Switching chats reuses it, rebuilding only
// the right pane; leaving the page detaches its layout, so the next visit
// reads the list afresh.
let page = null;

export async function chatsView(ctx) {
  if (!page || page.layout.parentNode !== ctx.el) {
    const next = chatPage();
    await next.load(ctx.signal);
    if (ctx.signal.aborted) return;
    page = next;
    ctx.show(page.layout);
  }
  page.select(ctx.params.key || '', ctx.signal);
}
