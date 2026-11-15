import { h, mount, errorBox, loading, field } from '../lib/dom.js';
import { get, post, put, request, convPath, isAbort } from '../lib/api.js';
import { go, href } from '../lib/router.js';
import { fmtTime, fmtClock } from '../lib/format.js';
import { chatPane } from './chat.js';

// PANEL is the source of a bot created in the panel and the platform of the
// chats held in the panel.
const PANEL = 'panel';

// MAX_DISPLAY_NAME is the longest name a panel chat calls its user by.
const MAX_DISPLAY_NAME = 32;

const PANEL_HINT = '在面板里和 bot 私聊，你是这个聊天的管理员。bot 的回复只出现在面板里。每条最多 2000 字。';

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

// botDialog creates a panel bot or edits one: its name, the persona template
// its new chats start from and its skills. saved(bot, isNew) runs after a
// save.
function botDialog(saved) {
  const dialog = h('dialog', { class: 'card dialog wide' });
  // open shows the form for bot, or for a new bot when bot is null, with the
  // templates and skills to choose from. bots are all the bots: a name one of
  // them already has is pointed out, not refused.
  const open = (bot, { templates, skills, bots }) => {
    const name = h('input', { required: true, value: bot ? bot.name : '' });
    const same = h('p', { class: 'hint warn', hidden: true });
    const checkName = () => {
      const v = name.value.trim();
      const others = bots.filter((b) => (!bot || b.id !== bot.id) && b.name === v);
      same.hidden = !v || others.length === 0;
      same.textContent = `已有同名的 bot：${others.map(botLabel).join('、')}。可以重名，只是不好区分。`;
    };
    name.addEventListener('input', checkName);
    checkName();
    const current = bot ? bot.persona_template : 'default';
    const names = templates.includes(current) ? templates : [current, ...templates];
    const template = h('select', null, names.map((t) => h('option', { value: t, selected: t === current }, t)));
    const boxes = skills.map((sk) => ({
      name: sk.name,
      box: h('input', { type: 'checkbox', checked: bot ? (bot.skills || []).includes(sk.name) : true }),
      description: sk.description,
    }));
    const msg = h('div');
    const save = h('button', { type: 'submit', class: 'primary' }, bot ? '保存' : '新建');
    const cancel = h('button', { type: 'button', onclick: () => dialog.close() }, '取消');
    mount(dialog, h('form', {
      onsubmit: async (e) => {
        e.preventDefault();
        save.disabled = true;
        mount(msg);
        const body = {
          name: name.value.trim(),
          persona_template: template.value,
          skills: boxes.filter((b) => b.box.checked).map((b) => b.name),
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
    field('名字', name, '1–32 字，不能有换行和不可见字符'),
    same,
    field('默认人设模板', template, bot
      ? '只影响之后新建的聊天，以及 bot 还没回复过的聊天；已有聊天的人设在会话的“人设”页里改'
      : '新建的聊天从这个模板开始'),
    h('fieldset', { class: 'skills' },
      h('legend', null, '技能'),
      boxes.length
        ? boxes.map((b) => h('label', { class: 'skill' }, b.box,
          h('span', null, h('strong', null, b.name), b.description ? h('span', { class: 'hint' }, b.description) : null)))
        : h('p', { class: 'muted' }, '没有可以在私聊里用的技能')),
    h('p', { class: 'hint' }, bot ? '改名字和技能从下一次回复起生效。' : '技能默认全选，可以一个都不选。'),
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
// left, the chat chosen on the right. load reads the list; select(key,
// signal) shows the chat key, or a hint when key is empty, and keeps the list.
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

  const loadSkills = () => {
    if (!skills) {
      skills = get('bots/skills').then((res) => res.skills || []);
      skills.catch(() => {
        skills = null;
      });
    }
    return skills;
  };

  const editBot = async (bot) => {
    mount(listMsg);
    try {
      botForm.open(bot, { templates, skills: await loadSkills(), bots });
    } catch (err) {
      if (!isAbort(err)) mount(listMsg, errorBox(err));
    }
  };

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
        panelBot && bot ? [
          h('button', { type: 'button', class: 'link', onclick: () => editBot(bot) }, '编辑'),
          h('button', {
            type: 'button',
            class: 'link danger',
            onclick: () => {
              mount(listMsg);
              deleteForm.open(bot);
            },
          }, '删除'),
        ] : null),
      panelBot ? null : h('p', { class: 'hint' }, '在配置文件里修改'),
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
        h('p', null, '从左边选一个聊天，或者在某个 bot 下点“新建聊天”。')));
      return;
    }
    const shown = find(key);
    let head = shown ? chatHead(key, shown.chat.title || key, shown.group.name || shown.group.id) : null;
    mount(pane, head, loading());
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
      if (!signal.aborted) mount(pane, head, nodes);
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
    chatDialog.dialog, botForm.dialog, deleteForm.dialog);

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
