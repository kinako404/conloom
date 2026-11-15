// The persona form shared by a conversation's persona tab and the persona
// templates page.

import { h, mount, errorBox, field, select } from '../lib/dom.js';
import { post, upload, isAbort } from '../lib/api.js';

const REPLY_LENGTH = { short: '短（short）', medium: '中（medium）', long: '长（long）' };
const FOLLOWUP = { never: '从不（never）', occasionally: '偶尔（occasionally）', often: '经常（often）' };
const TECHNICAL = { brief: '简要（brief）', clear_and_practical: '清楚实用（clear_and_practical）', detailed: '详细（detailed）' };
const STYLE = [
  ['warmth', '亲和'],
  ['humor', '幽默'],
  ['directness', '直接'],
  ['verbosity', '话多'],
  ['emoji_usage', '表情'],
  ['proactivity', '主动'],
];
const MAX_EXAMPLES = 8;
// MAX_BARS is conloomapi.MaxPersonaBars, the most bars a persona has.
const MAX_BARS = 4;
// MAX_APPEARANCE is conloomapi.MaxAppearanceRunes, the longest appearance text.
const MAX_APPEARANCE = 800;
// MAX_IMAGE_MIB is imaging.MaxBytes in MiB, the largest image upload.
const MAX_IMAGE_MIB = 8;

function fail(box, message) {
  mount(box, errorBox({ message }));
}

// shortDuration drops the zero minutes and seconds of a Go duration, "6h0m0s"
// becoming "6h".
function shortDuration(v) {
  return String(v || '').replace(/([hm])0s$/, '$1').replace(/h0m$/, 'h');
}

// barsEditor edits a persona's bars, one row each, at most MAX_BARS. read
// returns {bars}, keeping fields the rows do not show, or {error}.
function barsEditor(bars) {
  const list = h('div');
  const inputs = new WeakMap();
  const add = h('button', { type: 'button' }, '添加状态条');
  const updateAdd = () => {
    add.disabled = list.children.length >= MAX_BARS;
  };
  const addBar = (bar) => {
    const name = h('input', { value: bar.name || '' });
    const baseline = h('input', { type: 'number', min: 0, max: 100, step: 1, value: bar.baseline ?? '' });
    const halfLife = h('input', { value: shortDuration(bar.half_life), placeholder: '6h' });
    const description = h('input', { value: bar.description || '' });
    const row = h('div', { class: 'card' },
      h('div', { class: 'form-grid' },
        field('名称', name, '1–6 字，不能重复，不含空白、【】和冒号'),
        field('基准', baseline, '0–100 的整数，数值会逐渐回到这里'),
        field('半衰期', halfLife, '30m–168h，如 6h、90m'),
        field('说明', description, '可选，最多 40 字，如“高=有精神，低=累、困”')),
      h('button', {
        type: 'button',
        class: 'danger',
        onclick: () => {
          row.remove();
          updateAdd();
        },
      }, '删除状态条'));
    inputs.set(row, { bar, name, baseline, halfLife, description });
    list.appendChild(row);
    updateAdd();
  };
  add.addEventListener('click', () => addBar({}));
  for (const bar of bars || []) addBar(bar);
  const read = () => {
    const out = [];
    for (const [i, row] of [...list.children].entries()) {
      const r = inputs.get(row);
      const baseline = Number(r.baseline.value);
      if (r.baseline.value.trim() === '' || !Number.isInteger(baseline)) return { error: `第 ${i + 1} 条状态条的基准必须是整数` };
      out.push({
        ...r.bar,
        name: r.name.value.trim(),
        baseline,
        half_life: r.halfLife.value.trim(),
        description: r.description.value.trim(),
      });
    }
    return { bars: out };
  };
  return { el: [list, h('div', { class: 'toolbar' }, add)], read };
}

// imageURL is the address of the reply copy of the persona image id, the copy
// the reply model sees. v gives every image its own address, so the browser
// never shows an earlier image in its place.
function imageURL(id) {
  const v = encodeURIComponent(id);
  return `api/persona-images/${v}?size=reply&v=${v}`;
}

// appearanceEditor edits a persona's appearance: its text, its reference
// image and whether replies carry the image. An upload is stored on the
// server at once, but the persona refers to it only once saved; a generated
// description only fills the text box. read returns {fields}, the three
// fields, or {error} while an upload or a description is under way.
function appearanceEditor(base) {
  let imageId = base.appearance_image || '';
  let busy = false;
  const text = h('textarea', { value: base.appearance || '' });
  const attach = h('input', { type: 'checkbox', checked: !base.appearance_image_skip });
  const msg = h('div');
  const preview = h('img', { class: 'persona-image', alt: '外观参考图' });
  const none = h('p', { class: 'muted' }, '没有参考图');
  const file = h('input', { type: 'file', accept: 'image/png,image/jpeg', hidden: true });
  const choose = h('button', { type: 'button', onclick: () => file.click() }, '上传图片');
  const describe = h('button', { type: 'button' }, '生成外观介绍');
  const remove = h('button', { type: 'button', class: 'danger' }, '移除图片');

  const sync = () => {
    choose.disabled = busy;
    describe.disabled = busy || !imageId;
    remove.disabled = busy || !imageId;
  };
  const show = (src) => {
    if (src) preview.setAttribute('src', src);
    else preview.removeAttribute('src');
    preview.hidden = !src;
    none.hidden = !!src;
  };
  // A stored image that does not load may have been purged; a local preview
  // that does not load is left to the upload's own error.
  preview.addEventListener('error', () => {
    if ((preview.getAttribute('src') || '').startsWith('api/')) {
      fail(msg, '参考图读取失败，可能已被清理。请重新上传，或移除后再保存。');
    }
  });

  file.addEventListener('change', async () => {
    const f = file.files[0];
    file.value = '';
    if (!f) return;
    if (f.size > MAX_IMAGE_MIB * 1024 * 1024) {
      fail(msg, `图片不能超过 ${MAX_IMAGE_MIB} MiB`);
      return;
    }
    busy = true;
    sync();
    mount(msg, h('p', { class: 'muted' }, '正在上传…'));
    const reader = new FileReader();
    reader.addEventListener('load', () => {
      if (busy) show(reader.result);
    });
    reader.readAsDataURL(f);
    try {
      const res = await upload('persona-images', f);
      imageId = res.id;
      show(imageURL(imageId));
      mount(msg, h('div', { class: 'notice' },
        `已上传（${res.width}×${res.height}，回复时约占 ${res.reply_tokens} token）。点保存后才写进人设。`));
    } catch (err) {
      show(imageId ? imageURL(imageId) : '');
      if (!isAbort(err)) mount(msg, errorBox(err));
    } finally {
      busy = false;
      sync();
    }
  });

  describe.addEventListener('click', async () => {
    if (text.value.trim() && !confirm('生成的外观介绍会替换外观描述框里现有的内容（点保存前不生效）。继续？')) return;
    busy = true;
    sync();
    mount(msg, h('p', { class: 'muted' }, '识图模型正在看图，最多约 40 秒…'));
    try {
      const res = await post(`persona-images/${encodeURIComponent(imageId)}/describe`);
      text.value = res.text || '';
      mount(msg, h('div', { class: 'notice' }, '已填进外观描述。确认或修改后点保存。'));
    } catch (err) {
      if (!isAbort(err)) mount(msg, errorBox(err));
    } finally {
      busy = false;
      sync();
    }
  });

  remove.addEventListener('click', () => {
    imageId = '';
    show('');
    mount(msg, h('div', { class: 'notice' }, '已移除参考图，点保存后生效。'));
    sync();
  });

  show(imageId ? imageURL(imageId) : '');
  sync();
  const read = () => (busy
    ? { error: '参考图还在上传或生成外观介绍，完成后再保存' }
    : { fields: { appearance: text.value, appearance_image: imageId, appearance_image_skip: !attach.checked } });
  return {
    el: [
      field('外观描述', text, `可选，最多 ${MAX_APPEARANCE} 字；不为空时作为“外貌”一段写进系统提示`),
      h('div', { class: 'field' }, h('span', { class: 'field-label' }, '参考图'), preview, none),
      h('div', { class: 'toolbar' }, choose, describe, remove, file),
      h('p', { class: 'hint' },
        `PNG 或 JPEG，最多 ${MAX_IMAGE_MIB} MiB，宽和高不超过 8000 像素、总共不超过 2500 万像素。上传后图存在服务器上，点保存才写进人设；没有人设用到的图 7 天后清理。` +
        '“生成外观介绍”让识图模型看这张图写一段外观描述，填进上面的框里，不会自动保存。'),
      msg,
      h('label', { class: 'field inline' }, attach, '回复时附带外观图'),
      h('p', { class: 'hint' }, '勾选且有参考图时，回复模型支持图片的话，回复请求里带上这张图的缩小版，让角色知道自己的样子。'),
    ],
    read,
  };
}

// personaForm edits doc, a persona, keeping the fields the form does not
// show. Submitting (the button saveText) reads the persona and calls
// save(persona, reason, msg, button), which stores it and shows the outcome
// in msg; read() reads the persona without saving, as {persona} or {error}.
export function personaForm(doc, { save, saveText = '保存人设' }) {
  const base = structuredClone(doc || {});
  const style = base.style || {};
  const behavior = base.behavior || {};

  const name = h('input', { value: base.persona_name || '' });
  const templateVersion = h('input', { type: 'number', min: 0, step: 1, value: base.template_version ?? 0 });
  const characterName = h('input', { value: base.character_name || '' });
  const description = h('textarea', { value: base.description || '' });
  const speakingStyle = h('textarea', { value: base.speaking_style || '' });
  const appearance = appearanceEditor(base);
  const styleInputs = Object.fromEntries(STYLE.map(([k]) => [k,
    h('input', { type: 'number', min: 0, max: 1, step: 'any', value: style[k] ?? '' })]));
  const replyLength = select(REPLY_LENGTH, behavior.default_reply_length);
  const followup = select(FOLLOWUP, behavior.ask_followup_questions);
  const technical = select(TECHNICAL, behavior.technical_explanations);
  const memes = h('input', { type: 'checkbox', checked: !!behavior.allow_memes });
  const minor = h('input', { type: 'checkbox', checked: !!base.minor });
  const adult = h('input', { type: 'checkbox', checked: !!base.adult });
  const adultNote = h('span', { class: 'hint' }, '未成年角色不能设为成年人设');
  const syncAdult = () => {
    if (minor.checked) adult.checked = false;
    adult.disabled = minor.checked;
    adultNote.hidden = !minor.checked;
  };
  minor.addEventListener('change', syncAdult);
  syncAdult();
  const contentRules = h('textarea', { value: base.content_rules || '' });
  const memoryRules = h('textarea', { value: base.memory_rules || '' });

  const examples = h('div');
  const exampleInputs = new WeakMap();
  const addButton = h('button', { type: 'button' }, '添加示例');
  const updateAdd = () => {
    addButton.disabled = examples.children.length >= MAX_EXAMPLES;
  };
  const addExample = (ex) => {
    const user = h('textarea', { value: ex.user || '' });
    const reply = h('textarea', { value: ex.reply || '' });
    const row = h('div', { class: 'card' },
      h('div', { class: 'cols' }, field('群友说', user), field('回复', reply)),
      h('button', {
        type: 'button',
        class: 'danger',
        onclick: () => {
          row.remove();
          updateAdd();
        },
      }, '删除示例'));
    exampleInputs.set(row, { user, reply });
    examples.appendChild(row);
    updateAdd();
  };
  addButton.addEventListener('click', () => addExample({}));
  for (const ex of base.examples || []) addExample(ex);
  const bars = barsEditor(base.bars);

  const read = () => {
    const tv = Number(templateVersion.value);
    if (templateVersion.value.trim() === '' || !Number.isInteger(tv)) return { error: '模板版本必须是整数' };
    const barRows = bars.read();
    if (barRows.error) return { error: barRows.error };
    const look = appearance.read();
    if (look.error) return { error: look.error };
    const persona = structuredClone(base);
    persona.persona_name = name.value;
    persona.template_version = tv;
    persona.style = { ...style };
    for (const [k, title] of STYLE) {
      const v = styleInputs[k].valueAsNumber;
      if (Number.isNaN(v)) return { error: `风格「${title}」必须是 0 到 1 之间的数` };
      persona.style[k] = v;
    }
    persona.behavior = {
      ...behavior,
      default_reply_length: replyLength.value,
      allow_memes: memes.checked,
      ask_followup_questions: followup.value,
      technical_explanations: technical.value,
    };
    persona.character_name = characterName.value;
    persona.description = description.value;
    Object.assign(persona, look.fields);
    persona.speaking_style = speakingStyle.value;
    persona.minor = minor.checked;
    persona.adult = adult.checked;
    persona.content_rules = contentRules.value;
    persona.memory_rules = memoryRules.value;
    persona.examples = [...examples.children].map((row) => {
      const ex = exampleInputs.get(row);
      return { user: ex.user.value, reply: ex.reply.value };
    });
    if (barRows.bars.length) persona.bars = barRows.bars;
    else delete persona.bars;
    return { persona };
  };

  const reason = h('input', { placeholder: '修改原因' });
  const msg = h('div');
  const submit = h('button', { type: 'submit', class: 'primary' }, saveText);
  const el = h('form', {
    onsubmit: (e) => {
      e.preventDefault();
      const r = read();
      if (r.error) return fail(msg, r.error);
      return save(r.persona, reason.value.trim(), msg, submit);
    },
  },
  h('div', { class: 'form-grid' },
    field('人设名', name),
    field('模板版本', templateVersion),
    field('角色名', characterName, '可选，最多 20 字；设置后就是 bot 在会话里的名字')),
  field('描述', description, '可选，最多 2000 字'),
  field('说话风格', speakingStyle, '可选，最多 1000 字'),
  h('h3', null, '外观'),
  appearance.el,
  h('h3', null, '风格（0–1）'),
  h('div', { class: 'form-grid' }, STYLE.map(([k, title]) => field(`${title}（${k}）`, styleInputs[k]))),
  h('h3', null, '行为'),
  h('div', { class: 'form-grid' },
    field('回复长度', replyLength),
    field('追问', followup),
    field('技术解释', technical),
    h('label', { class: 'field inline' }, memes, '允许玩梗')),
  h('h3', null, '内容尺度'),
  h('p', { class: 'hint' }, '只有成年、非未成年的人设在私聊里是开放模式，下面两项规则只在开放模式生效；其余情况都是保护模式，使用内置的保护规则，不能通过配置去掉。'),
  h('div', { class: 'form-grid' },
    h('label', { class: 'field inline' }, minor, '未成年角色'),
    h('label', { class: 'field inline' }, adult, '成年人设', adultNote)),
  field('内容规则（仅成年人设、私聊生效）', contentRules, '可选，最多 2000 字；开放模式下加入回复提示词，为空则不加'),
  field('记忆规则（仅成年人设、私聊生效）', memoryRules, '可选，最多 1000 字；开放模式下替换记忆整理的第 6 条，为空则省略该条'),
  h('h3', null, `示例对话（最多 ${MAX_EXAMPLES} 条，每条最多 200 字）`),
  examples,
  h('div', { class: 'toolbar' }, addButton),
  h('h3', null, `状态条（最多 ${MAX_BARS} 条）`),
  h('p', { class: 'hint' }, '私聊里只有 bot 自己知道的数值（0–100），只影响语气和想不想多聊，不进系统提示；bot 启用了 buff 技能才生效。一条都不写时用默认的心情和精力。'),
  bars.el,
  field('原因', reason),
  msg, submit);
  return { el, read };
}
