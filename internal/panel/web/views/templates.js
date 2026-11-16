import { h, mount, errorBox, empty, table, kv, field } from '../lib/dom.js';
import { get, post, put, del, upload, isAbort } from '../lib/api.js';
import { go, href } from '../lib/router.js';
import { fmtTime } from '../lib/format.js';
import { personaForm } from './personaform.js';

// BUILTIN is the source of the built-in default persona, shown while no
// default is stored (persona.TemplateSource).
const BUILTIN = 'builtin';

// TEMPLATE_NAME is the form of a template name.
const TEMPLATE_NAME = /^[a-z0-9_-]{1,32}$/;

// RESERVED_NAMES are the names no new template may have
// (persona.reservedTemplateNames): the API's export and import use them.
const RESERVED_NAMES = ['export', 'import'];

// BUILTIN_DEFAULT is the built-in default persona, where a blank template
// starts, also after default was saved.
const BUILTIN_DEFAULT = 'persona-templates/default/builtin';

// BLANK is the start of a new template that is the built-in default persona.
const BLANK = '';

// MAX_IMPORT_MIB is the largest file an import takes.
const MAX_IMPORT_MIB = 64;

// MAX_IMPORT_TEMPLATES is the most templates an imported file may hold.
const MAX_IMPORT_TEMPLATES = 200;

// EXPORT_ALL downloads every template as one file.
const EXPORT_ALL = 'api/persona-templates/export';

// TEMPLATE_EXISTS is the error code of an import some of whose names stored
// templates have; the error's names lists them.
const TEMPLATE_EXISTS = 'TEMPLATE_EXISTS';

const EXISTING_CHATS = '改模板不影响已经在用它的会话：会话第一次回复时复制一份模板，之后各自独立。要更新已有会话，到会话的“人设”页点“套用模板”。';

// flash is a success message, text or nodes, shown once after a write moves
// to a page.
let flash = '';

function takeFlash() {
  const note = flash ? h('div', { class: 'notice' }, flash) : null;
  flash = '';
  return note;
}

// write runs a change, showing its error in box, and goes to hash (the page
// itself by default) with done as the success message, or with done(result)
// when done is a function of the change's result.
async function write(box, button, fn, done, hash = location.hash) {
  if (button) button.disabled = true;
  mount(box);
  try {
    const res = await fn();
    flash = typeof done === 'function' ? done(res) : done;
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

// exportLink downloads template name as a file the server names.
function exportLink(name, attrs) {
  return h('a', { ...attrs, href: 'api/' + templatePath(name, 'export'), download: true }, '导出');
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

// exists reports whether the catalog has a template name. It reads the list:
// persona-templates/export is the download of all templates, not a template.
async function exists(name) {
  const res = await get('persona-templates');
  return (res.templates || []).some((t) => t.name === name);
}

// nameProblem says why a new template cannot be called name, or '' when it
// can.
function nameProblem(name) {
  if (!TEMPLATE_NAME.test(name)) return '模板名只能是 1–32 个小写字母、数字、- 或 _';
  if (RESERVED_NAMES.includes(name)) return '模板名不能是 export 或 import，这两个名字留给导出和导入';
  return '';
}

// createTemplate stores persona as the new template to, with reason for the
// audit; a name a template already has is refused, not overwritten.
async function createTemplate(to, persona, reason) {
  if (await exists(to)) throw new Error(`已经有名为 ${to} 的模板`);
  await put(templatePath(to), { persona, reason });
}

// deletedNote is the message after template name was deleted: res, the
// response, names the bots that used it, which use default from now on.
function deletedNote(name, res) {
  const bots = (res && res.affected_bots) || [];
  if (!bots.length) return `已删除模板 ${name}`;
  return `已删除模板 ${name}。这些 bot 把它当默认模板，现在改用 default：${bots.join('、')}`;
}

// newTemplateDialog creates a template from the name and start chosen in it,
// the built-in default persona or a copy of the saved version of one of
// templates, and opens it. open() shows it.
function newTemplateDialog(templates) {
  const dialog = h('dialog', { class: 'card dialog' });
  const name = h('input', { required: true, autocomplete: 'off', maxlength: 32 });
  const from = h('select', null,
    h('option', { value: BLANK }, '空白（内置默认人设）'),
    templates.map((t) => h('option', { value: t.name }, `复制 ${t.name}`)));
  const msg = h('div');
  const create = h('button', { type: 'submit', class: 'primary' }, '新建');
  const cancel = h('button', { type: 'button', onclick: () => dialog.close() }, '取消');
  mount(dialog, h('form', {
    onsubmit: async (e) => {
      e.preventDefault();
      const to = name.value.trim();
      const problem = nameProblem(to);
      if (problem) {
        fail(msg, problem);
        return;
      }
      const start = from.value;
      create.disabled = true;
      mount(msg);
      try {
        const persona = start === BLANK
          ? (await get(BUILTIN_DEFAULT)).persona
          : (await get(templatePath(start))).persona;
        await createTemplate(to, persona, start === BLANK ? '从内置默认人设新建' : `复制自 ${start}`);
        dialog.close();
        flash = `已新建模板 ${to}`;
        go(href('templates', to));
      } catch (err) {
        if (!isAbort(err)) mount(msg, errorBox(err));
      } finally {
        create.disabled = false;
      }
    },
  },
  h('h2', null, '新建模板'),
  field('模板名', name, '1–32 个小写字母、数字、- 或 _，不能是 export 或 import，不能和已有模板重名'),
  field('起点', from, '空白就是内置的默认人设；复制时用那个模板保存过的版本，原模板不变'),
  msg,
  h('div', { class: 'toolbar' }, create, cancel)));
  const open = () => {
    name.value = '';
    from.value = BLANK;
    mount(msg);
    dialog.showModal();
  };
  return { dialog, open };
}

// importSummary tells under which names an import stored its templates,
// created or overwritten, with the file's name of a renamed one.
function importSummary(list) {
  const named = (t) => (t.name === t.requested ? t.name : `${t.name}（文件里是 ${t.requested}）`);
  const created = list.filter((t) => t.created).map(named);
  const overwritten = list.filter((t) => !t.created).map(named);
  return [
    h('div', null, `已导入 ${list.length} 个模板。`),
    created.length ? h('div', null, `新建：${created.join('、')}`) : null,
    overwritten.length ? h('div', null, `覆盖：${overwritten.join('、')}`) : null,
  ];
}

// importer imports an exported file: the 导入 button picks it, msg shows the
// progress and errors, and when names in the file are taken the dialog asks
// whether to overwrite those templates or import under new names. done(list)
// runs with the imported templates. Esc does not close the dialog while its
// import runs; should the browser close it anyway, msg shows the progress
// and any error instead.
function importer(done) {
  const msg = h('div');
  const file = h('input', { type: 'file', accept: 'application/json,.json', hidden: true });
  const button = h('button', { type: 'button', onclick: () => file.click() }, '导入');
  const dialog = h('dialog', { class: 'card dialog' });
  let busy = false;
  dialog.addEventListener('cancel', (e) => {
    if (busy) e.preventDefault();
  });
  dialog.addEventListener('close', () => {
    if (busy) mount(msg, h('p', { class: 'muted' }, '正在导入…'));
  });

  const send = async (body, mode) => {
    const res = await upload('persona-templates/import', body, { on_conflict: mode });
    done(res.templates || []);
  };

  const ask = (body, names) => {
    const status = h('div');
    const cancel = h('button', { type: 'button', autofocus: true, onclick: () => dialog.close() }, '取消');
    const overwrite = h('button', { type: 'button', class: 'danger' }, '覆盖');
    const rename = h('button', { type: 'button' }, '改名导入');
    const choose = (mode) => async () => {
      busy = true;
      overwrite.disabled = true;
      rename.disabled = true;
      cancel.disabled = true;
      mount(status, h('p', { class: 'muted' }, '正在导入…'));
      try {
        await send(body, mode);
        dialog.close();
      } catch (err) {
        if (!isAbort(err)) mount(dialog.open ? status : msg, errorBox(err));
      } finally {
        busy = false;
        overwrite.disabled = false;
        rename.disabled = false;
        cancel.disabled = false;
      }
    };
    overwrite.addEventListener('click', choose('overwrite'));
    rename.addEventListener('click', choose('rename'));
    mount(dialog,
      h('h2', null, '有同名的模板'),
      h('p', null, '文件里这些模板的名字已经有模板在用：'),
      h('ul', { class: 'names' }, names.map((n) => h('li', { class: 'mono' }, n))),
      h('p', { class: 'hint' }, '覆盖：用文件里的内容替换这些模板。改名导入：这些模板以“名字-2”这样没被占用的名字导入，已有的模板不变。文件里的其他模板照常导入；取消则什么都不导入。'),
      status,
      h('div', { class: 'toolbar' }, overwrite, rename, cancel));
    dialog.showModal();
  };

  file.addEventListener('change', async () => {
    const f = file.files[0];
    file.value = '';
    if (!f) return;
    if (f.size > MAX_IMPORT_MIB * 1024 * 1024) {
      fail(msg, `导入文件不能超过 ${MAX_IMPORT_MIB} MiB`);
      return;
    }
    button.disabled = true;
    mount(msg, h('p', { class: 'muted' }, `正在导入 ${f.name}…`));
    try {
      let text;
      try {
        text = await f.text();
      } catch (err) {
        fail(msg, `读取文件失败：${err.message}`);
        return;
      }
      const body = new Blob([text], { type: 'application/json' });
      try {
        await send(body, 'fail');
      } catch (err) {
        if (err.status !== 409 || err.code !== TEMPLATE_EXISTS) throw err;
        mount(msg);
        ask(body, err.names);
      }
    } catch (err) {
      if (!isAbort(err)) mount(msg, errorBox(err));
    } finally {
      button.disabled = false;
    }
  });

  return { button, file, msg, dialog };
}

export async function templatesView(ctx) {
  const res = await get('persona-templates', null, ctx.signal);
  const all = res.templates || [];
  const imp = importer((list) => {
    if (ctx.signal.aborted) return;
    flash = importSummary(list);
    go('#/templates');
  });
  const create = newTemplateDialog(all);
  ctx.show(
    h('h1', null, '人设模板'),
    takeFlash(),
    h('div', { class: 'toolbar' },
      h('button', { type: 'button', class: 'primary', onclick: create.open }, '新建模板'),
      h('a', { class: 'button', href: EXPORT_ALL, download: true }, '导出全部'),
      imp.button, imp.file),
    h('p', { class: 'hint' }, `导出的是 JSON 文件，参考图在文件里。导入时选导出的文件，单个模板或全部模板的都可以，最大 ${MAX_IMPORT_MIB} MiB、最多 ${MAX_IMPORT_TEMPLATES} 个模板；模板多、参考图大时，“导出全部”的文件可能超过这个上限，没法整个导入，这时请逐个导出。`),
    imp.msg,
    h('p', { class: 'hint' }, EXISTING_CHATS),
    all.length ? table([
      {
        title: '模板',
        cell: (t) => [
          h('a', { href: href('templates', t.name) }, t.name),
          t.source === BUILTIN ? [' ', h('span', { class: 'badge', title: '内置的默认人设，还没有保存过' }, '内置')] : null,
        ],
      },
      { title: '人设名', cell: (t) => t.persona_name || '—' },
      { title: '角色名', cell: (t) => t.character_name || '—' },
      { title: '参考图', cell: (t) => (t.appearance_image ? '有' : '—') },
      { title: '在线修改', class: 'nowrap', cell: updated },
      { title: '导出', class: 'nowrap', cell: (t) => exportLink(t.name) },
    ], all) : empty('没有模板'),
    imp.dialog, create.dialog);
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
      const usedBy = (t.users || []).map(botLabel);
      const question = usedBy.length
        ? `删除模板 ${name}？这些 bot 把它当默认模板，删除后改用 default：${usedBy.join('、')}。已经用它的会话不受影响。`
        : `删除模板 ${name}？已经用它的会话不受影响。`;
      if (!confirm(question)) return;
      write(actionMsg, e.currentTarget, () => del(templatePath(name)), (res) => deletedNote(name, res), '#/templates');
    },
  }, '删除') : null;

  const copy = h('button', {
    type: 'button',
    onclick: (e) => {
      const button = e.currentTarget;
      const r = form.read();
      if (r.error) return fail(actionMsg, r.error);
      const answer = prompt('新模板的名字：1–32 个小写字母、数字、- 或 _，不能是 export 或 import。新模板用下面表单里当前的内容，本模板不变。', '');
      if (answer === null) return undefined;
      const to = answer.trim();
      const problem = nameProblem(to);
      if (problem) return fail(actionMsg, problem);
      return write(actionMsg, button, () => createTemplate(to, r.persona, `复制自 ${name}`),
        `已复制为新模板 ${to}`, href('templates', to));
    },
  }, '复制为新模板');

  ctx.show(
    h('p', null, h('a', { href: '#/templates' }, '← 人设模板')),
    h('h1', null, `人设模板：${name}`),
    takeFlash(),
    h('section', { class: 'card' },
      kv([
        t.source === BUILTIN ? ['来源', '内置的默认人设，还没有保存过'] : null,
        ['默认模板用于', users(t)],
        ['在线修改', updated(t)],
      ]),
      h('p', { class: 'hint' }, EXISTING_CHATS),
      h('div', { class: 'toolbar' }, reset, remove, copy,
        exportLink(name, { class: 'button', title: '下载保存过的版本，表单里没保存的修改不在里面' })),
      actionMsg),
    h('section', { class: 'card' }, h('h2', null, '编辑模板'), form.el),
  );
}
