import { h, mount, errorBox, empty, table, kv } from '../lib/dom.js';
import { get, post, put, del, isAbort } from '../lib/api.js';
import { go, href } from '../lib/router.js';
import { fmtTime, label } from '../lib/format.js';
import { personaForm } from './personaform.js';

// SOURCE labels where a template comes from (persona.TemplateSource).
const SOURCE = { db: '数据库', builtin: '内置' };

// TEMPLATE_NAME is the form of a template name.
const TEMPLATE_NAME = /^[a-z0-9_-]{1,32}$/;

const EXISTING_CHATS = '改模板不影响已经在用它的会话：会话第一次回复时复制一份模板，之后各自独立。要更新已有会话，到会话的“人设”页点“套用模板”。';

// flash is a success message shown once after a write moves to a page.
let flash = '';

function takeFlash() {
  const note = flash ? h('div', { class: 'notice' }, flash) : null;
  flash = '';
  return note;
}

// write runs a change, showing its error in box, and goes to hash (the page
// itself by default) with done as the success message.
async function write(box, button, fn, done, hash = location.hash) {
  if (button) button.disabled = true;
  mount(box);
  try {
    await fn();
    flash = done;
    go(hash);
  } catch (err) {
    if (!isAbort(err)) mount(box, errorBox(err));
  } finally {
    if (button) button.disabled = false;
  }
}

function fail(box, message) {
  mount(box, errorBox({ message }));
}

function templatePath(name, ...rest) {
  return ['persona-templates', encodeURIComponent(name), ...rest].join('/');
}

// botLabel names a bot: a panel bot as 名字（面板）, a config bot by its name
// and ID.
function botLabel(b) {
  if (b.source === 'panel') return `${b.name}（面板）`;
  return b.name ? `${b.name}（${b.id}）` : b.id;
}

// users names the bots whose new chats start from template t.
function users(t) {
  const list = t.users || [];
  return list.length ? list.map(botLabel).join('、') : '—';
}

function updated(t) {
  return t.updated_at ? `${fmtTime(t.updated_at)}，${t.updated_by}` : '—';
}

// exists reports whether the catalog has a template name.
async function exists(name) {
  try {
    await get(templatePath(name));
    return true;
  } catch (err) {
    if (err.status === 404) return false;
    throw err;
  }
}

export async function templatesView(ctx) {
  const res = await get('persona-templates', null, ctx.signal);
  const all = res.templates || [];
  ctx.show(
    h('h1', null, '人设模板'),
    takeFlash(),
    h('p', { class: 'hint' }, EXISTING_CHATS),
    h('p', { class: 'hint' }, '新建模板：打开一个模板，点“复制为新模板”。'),
    all.length ? table([
      { title: '模板', cell: (t) => h('a', { href: href('templates', t.name) }, t.name) },
      { title: '人设名', cell: (t) => t.persona_name || '—' },
      { title: '角色名', cell: (t) => t.character_name || '—' },
      { title: '来源', cell: (t) => label(SOURCE, t.source) },
      { title: '参考图', cell: (t) => (t.appearance_image ? '有' : '—') },
      { title: '默认模板用于', class: 'text', cell: users },
      { title: '在线修改', class: 'nowrap', cell: updated },
    ], all) : empty('没有模板'));
}

export async function templateView(ctx) {
  const name = ctx.params.name;
  const t = await get(templatePath(name), null, ctx.signal);
  const actionMsg = h('div');

  const form = personaForm(t.persona, {
    saveText: '保存',
    save: (persona, reason, msg, button) => write(msg, button, () => put(templatePath(name), { persona, reason }), `模板 ${name} 已保存`),
  });

  const reset = name === 'default' ? h('button', {
    type: 'button',
    disabled: t.source !== 'db',
    title: t.source === 'db' ? null : '用的就是内置默认人设',
    onclick: (e) => {
      if (!confirm('删掉保存的 default，恢复为内置默认人设？')) return;
      write(actionMsg, e.currentTarget, () => post(templatePath(name, 'reset')), '模板 default 已恢复为内置默认');
    },
  }, '恢复为内置默认') : null;

  const remove = t.source === 'db' && name !== 'default' ? h('button', {
    type: 'button',
    class: 'danger',
    onclick: (e) => {
      if (!confirm(`删除模板 ${name}？已经用它的会话不受影响。`)) return;
      write(actionMsg, e.currentTarget, () => del(templatePath(name)), `已删除模板 ${name}`, '#/templates');
    },
  }, '删除') : null;

  const copy = h('button', {
    type: 'button',
    onclick: (e) => {
      const button = e.currentTarget;
      const r = form.read();
      if (r.error) return fail(actionMsg, r.error);
      const answer = prompt('新模板的名字：1–32 个小写字母、数字、- 或 _。新模板用下面表单里当前的内容，本模板不变。', '');
      if (answer === null) return undefined;
      const to = answer.trim();
      if (!TEMPLATE_NAME.test(to)) return fail(actionMsg, '模板名只能是 1–32 个小写字母、数字、- 或 _');
      return write(actionMsg, button, async () => {
        if (await exists(to)) throw new Error(`已经有名为 ${to} 的模板`);
        await put(templatePath(to), { persona: r.persona, reason: `复制自 ${name}` });
      }, `已复制为新模板 ${to}`, href('templates', to));
    },
  }, '复制为新模板');

  ctx.show(
    h('p', null, h('a', { href: '#/templates' }, '← 人设模板')),
    h('h1', null, `人设模板：${name}`),
    takeFlash(),
    h('section', { class: 'card' },
      kv([
        ['来源', label(SOURCE, t.source)],
        ['默认模板用于', users(t)],
        ['在线修改', updated(t)],
      ]),
      h('p', { class: 'hint' }, EXISTING_CHATS),
      h('div', { class: 'toolbar' }, reset, remove, copy),
      actionMsg),
    h('section', { class: 'card' }, h('h2', null, '编辑模板'), form.el),
  );
}
