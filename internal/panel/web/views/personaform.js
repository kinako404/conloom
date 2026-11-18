// The persona form shared by a conversation's persona tab and the persona
// templates page.

import { h, mount, errorBox, field, select } from '../lib/dom.js';
import { get, post, upload, isAbort } from '../lib/api.js';
import { imageURL, originalImageURL } from './avatar.js';
import { cropAvatar, AVATAR_SIZE } from './avatarcrop.js';

const REPLY_LENGTH = { short: '短（short）', medium: '中（medium）', long: '长（long）' };
const FOLLOWUP = { never: '从不（never）', occasionally: '偶尔（occasionally）', often: '经常（often）' };
const TECHNICAL = { brief: '简要（brief）', clear_and_practical: '清楚实用（clear_and_practical）', detailed: '详细（detailed）' };
// GENDER is the values of conloomapi.PersonaDoc.Gender.
const GENDER = { '': '不指定', female: '女', male: '男' };
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
// AVATAR_SOURCE_TYPES are the images an avatar is cropped from; the browser
// decodes them, and the crop is uploaded as a PNG or JPEG.
const AVATAR_SOURCE_TYPES = ['image/png', 'image/jpeg', 'image/webp', 'image/gif'];
// MAX_AVATAR_SOURCE_MIB bounds the image file an avatar is cropped from.
const MAX_AVATAR_SOURCE_MIB = 32;
// INTIMACY_LEVELS is the values of conloomapi.PersonaIntimacy.Levels, from
// mixing in none of the intimacy side to mixing in the most.
const INTIMACY_LEVELS = { none: '不混', light: '少量', half: '一半', major: '为主' };
// INTIMACY_TIERS names the affinity tiers from the lowest, a level each.
const INTIMACY_TIERS = ['陌生', '认识', '熟悉', '亲近', '很要好'];
// DEFAULT_INTIMACY_LEVELS is conloomapi.DefaultIntimacyLevels, the levels of
// an intimacy side without levels.
const DEFAULT_INTIMACY_LEVELS = ['none', 'none', 'light', 'half', 'major'];
// MAX_INTIMACY is conloomapi.MaxIntimacyRunes, the most characters the three
// texts of an intimacy side have together.
const MAX_INTIMACY = 800;
// MAX_INTIMACY_BOUND is conloomapi.MaxIntimacyBoundRunes, the most characters
// each bound of an intimacy side has.
const MAX_INTIMACY_BOUND = 150;

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

// appearanceEditor edits a persona's appearance: its text, its reference
// image and whether replies carry the image. An upload is stored on the
// server at once, but the persona refers to it only once saved; a generated
// description only fills the text box. onImage runs when the reference image
// changes, image() returns its ID. read returns {fields}, the three fields,
// or {error} while an upload or a description is under way.
function appearanceEditor(base, onImage) {
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
      onImage();
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
    onImage();
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
    image: () => imageId,
    read,
  };
}

// avatarEditor edits a persona template's avatar, the square image the chat
// page shows for the bots using the template, never sent to a model: a
// circle of the current one, and buttons to choose a square of an image file
// or of the reference image (reference() returns its ID, '' without one) in
// the crop dialog, which uploads it, and to remove it. Like the reference
// image, the avatar is uploaded at once but only saved with the form. sync()
// updates the buttons after the reference image changed. read returns
// {fields}, avatar_image, or {error} while the crop dialog is open. signal,
// the route's, closes the dialog when the page goes away.
function avatarEditor(base, reference, signal) {
  let avatarId = base.avatar_image || '';
  let busy = false;
  const msg = h('div');
  const current = h('img', { class: 'persona-avatar', alt: '头像', width: 96, height: 96 });
  const none = h('span', { class: 'persona-avatar persona-avatar-none' }, '未设置');
  const file = h('input', { type: 'file', accept: AVATAR_SOURCE_TYPES.join(','), hidden: true });
  const choose = h('button', { type: 'button', onclick: () => file.click() }, '上传头像');
  const fromReference = h('button', { type: 'button' }, '从参考图选取');
  const remove = h('button', { type: 'button', class: 'danger' }, '移除头像');
  const el = h('div', { class: 'persona-avatar-editor' });

  const sync = () => {
    choose.disabled = busy;
    fromReference.disabled = busy || !reference();
    remove.disabled = busy || !avatarId;
  };
  const show = () => {
    if (avatarId) current.setAttribute('src', imageURL(avatarId));
    else current.removeAttribute('src');
    current.hidden = !avatarId;
    none.hidden = !!avatarId;
  };
  current.addEventListener('error', () => {
    if (current.getAttribute('src')) fail(msg, '头像读取失败，可能已被清理。请重新选择，或移除后再保存。');
  });

  const crop = async (src) => {
    try {
      const res = await cropAvatar({ src, parent: el, maxBytes: MAX_IMAGE_MIB * 1024 * 1024, signal });
      if (res) {
        avatarId = res.id;
        show();
        mount(msg, h('div', { class: 'notice' }, '已设置头像，点保存后才写进人设。'));
      } else {
        mount(msg);
      }
    } catch (err) {
      if (!isAbort(err)) mount(msg, errorBox(err));
    } finally {
      busy = false;
      sync();
    }
  };
  const start = () => {
    busy = true;
    sync();
    mount(msg, h('p', { class: 'muted' }, '正在读取图片…'));
  };

  file.addEventListener('change', () => {
    const f = file.files[0];
    file.value = '';
    if (!f) return;
    if (f.type && !AVATAR_SOURCE_TYPES.includes(f.type)) {
      fail(msg, '只支持 PNG、JPEG、WebP 或 GIF 图片');
      return;
    }
    if (f.size > MAX_AVATAR_SOURCE_MIB * 1024 * 1024) {
      fail(msg, `图片不能超过 ${MAX_AVATAR_SOURCE_MIB} MiB`);
      return;
    }
    start();
    const reader = new FileReader();
    reader.addEventListener('load', () => crop(reader.result));
    reader.addEventListener('error', () => {
      busy = false;
      sync();
      fail(msg, '无法读取这个文件');
    });
    reader.readAsDataURL(f);
  });
  fromReference.addEventListener('click', () => {
    const id = reference();
    if (!id) return;
    start();
    crop(originalImageURL(id));
  });
  remove.addEventListener('click', () => {
    avatarId = '';
    show();
    mount(msg, h('div', { class: 'notice' }, '已移除头像，点保存后生效。'));
    sync();
  });

  show();
  sync();
  mount(el,
    h('div', { class: 'persona-avatar-row' }, current, none,
      h('div', { class: 'toolbar' }, choose, fromReference, remove, file)),
    h('p', { class: 'hint' },
      `PNG、JPEG、WebP 或 GIF，最多 ${MAX_AVATAR_SOURCE_MIB} MiB。选好图后框出一个正方形，裁成 ${AVATAR_SIZE}×${AVATAR_SIZE} 上传。` +
      '头像只在面板的聊天页显示，不发给模型；没有头像时显示参考图。'),
    msg);
  return {
    el,
    sync,
    read: () => (busy ? { error: '头像还在裁剪或上传，完成后再保存' } : { fields: { avatar_image: avatarId } }),
  };
}

// intimacyEditor edits a persona's intimacy side: its three texts, counted
// together after trimming, and a level and a bound per affinity tier, the
// bounds a full-width row each under the levels. A bound input's placeholder
// and title are the tier's built-in bound, read from the server once; a bound
// loses the line breaks it was pasted with. minor and adult are the
// form's checkboxes: a minor persona gets the section disabled, and while it
// still has text or a bound a button, outside the section, that empties the
// three texts and the bounds; one that is not adult a note that the section
// takes no effect. read returns {value}, the intimacy side, without bounds
// when they are all blank, or undefined when the three texts and the bounds
// are blank, or {error}.
function intimacyEditor(base, { minor, adult }) {
  const doc = base.intimacy || {};
  const tone = h('textarea', { value: doc.tone || '' });
  const mind = h('textarea', { value: doc.mind || '' });
  const behavior = h('textarea', { value: doc.behavior || '' });
  const texts = [tone, mind, behavior];
  const saved = doc.levels && doc.levels.length ? doc.levels : DEFAULT_INTIMACY_LEVELS;
  const levels = INTIMACY_TIERS.map((_, i) => select(INTIMACY_LEVELS, saved[i]));
  const savedBounds = doc.bounds || [];
  const bounds = INTIMACY_TIERS.map((_, i) => h('input', { type: 'text', value: savedBounds[i] || '' }));
  const showDefault = (b, text) => {
    b.placeholder = text;
    b.title = text;
  };
  get('affinity-bounds').then((res) => {
    const defaults = (res && res.defaults) || [];
    for (const [i, b] of bounds.entries()) showDefault(b, defaults[i] || '');
  }, () => {
    for (const b of bounds) showDefault(b, '空着就用内置边界');
  });
  const boundValues = () => bounds.map((b) => b.value.replace(/[\r\n]/g, ''));
  const hasBound = () => boundValues().some((v) => v.trim() !== '');
  const total = () => texts.reduce((n, t) => n + [...t.value.trim()].length, 0);
  const count = h('p', { class: 'hint' });
  const showCount = () => {
    const n = total();
    count.textContent = `合计 ${n} / ${MAX_INTIMACY} 字`;
    count.classList.toggle('warn', n > MAX_INTIMACY);
  };
  for (const t of texts) t.addEventListener('input', showCount);
  showCount();

  const minorNote = h('p', { class: 'hint warn' }, '未成年人设不能设置亲密面');
  const clear = h('button', { type: 'button', class: 'danger' }, '清空亲密面');
  const clearBar = h('div', { class: 'toolbar' }, clear);
  const notAdultNote = h('p', { class: 'hint warn' }, '当前不是成年人设，亲密面不会生效');
  const section = h('fieldset', { class: 'intimacy' },
    field('语气', tone),
    field('心理', mind),
    field('表现', behavior),
    count,
    h('div', { class: 'form-grid' }, INTIMACY_TIERS.map((tier, i) => field(tier, levels[i]))),
    INTIMACY_TIERS.map((tier, i) => field(`${tier} · 边界（可选）`, bounds[i])));
  const sync = () => {
    section.disabled = minor.checked;
    minorNote.hidden = !minor.checked;
    notAdultNote.hidden = minor.checked || adult.checked;
    clearBar.hidden = !minor.checked || (total() === 0 && !hasBound());
  };
  minor.addEventListener('change', sync);
  adult.addEventListener('change', sync);
  for (const t of texts) t.addEventListener('input', sync);
  for (const b of bounds) b.addEventListener('input', sync);
  clear.addEventListener('click', () => {
    for (const t of texts) t.value = '';
    for (const b of bounds) b.value = '';
    showCount();
    sync();
  });
  sync();

  const read = () => {
    const n = total();
    if (n === 0 && !hasBound()) return { value: undefined };
    if (minor.checked) return { error: '未成年人设不能设置亲密面，请先清空' };
    if (n > MAX_INTIMACY) return { error: `亲密面合计最多 ${MAX_INTIMACY} 字` };
    const written = boundValues();
    if (written.some((v) => [...v.trim()].length > MAX_INTIMACY_BOUND)) {
      return { error: `每档边界最多 ${MAX_INTIMACY_BOUND} 字` };
    }
    const order = Object.keys(INTIMACY_LEVELS);
    const chosen = levels.map((l) => l.value);
    if (chosen.some((v, i) => i > 0 && order.indexOf(v) < order.indexOf(chosen[i - 1]))) {
      return { error: '亲密面的档位要从低到高不递减' };
    }
    const value = { tone: tone.value, mind: mind.value, behavior: behavior.value, levels: chosen };
    if (hasBound()) value.bounds = written;
    return { value };
  };
  return {
    el: [
      h('p', { class: 'hint' }, '只在成年人设的私聊里生效；群聊和保护模式下不使用。改模板不影响已有会话，要到会话的人设页套用模板。',
        '边界替换这一档内置的分寸说明，空着就用内置的；它不能越过内容规则。'),
      minorNote,
      clearBar,
      notAdultNote,
      section,
    ],
    read,
  };
}

// personaForm edits doc, a persona, keeping the fields the form does not
// show. Submitting (the button saveText) reads the persona and calls
// save(persona, reason, msg, button), which stores it and shows the outcome
// in msg; read() reads the persona without saving, as {persona} or {error}.
// With avatar, as on the templates page, it also edits the avatar; signal,
// the route's, closes the crop dialog when the page goes away.
export function personaForm(doc, { save, saveText = '保存人设', avatar: withAvatar = false, signal }) {
  const base = JSON.parse(JSON.stringify(doc || {}));
  const style = base.style || {};
  const behavior = base.behavior || {};

  const name = h('input', { value: base.persona_name || '' });
  const templateVersion = h('input', { type: 'number', min: 0, step: 1, value: base.template_version ?? 0 });
  const characterName = h('input', { value: base.character_name || '' });
  const gender = select(GENDER, base.gender || '');
  const description = h('textarea', { value: base.description || '' });
  const speakingStyle = h('textarea', { value: base.speaking_style || '' });
  let avatar = null;
  const appearance = appearanceEditor(base, () => {
    if (avatar) avatar.sync();
  });
  avatar = withAvatar ? avatarEditor(base, appearance.image, signal) : null;
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
  const intimacy = intimacyEditor(base, { minor, adult });
  const contentRules = h('textarea', { value: base.content_rules || '' });
  const adultAppearance = h('textarea', { value: base.adult_appearance || '' });
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
    const face = avatar ? avatar.read() : { fields: {} };
    if (face.error) return { error: face.error };
    const side = intimacy.read();
    if (side.error) return { error: side.error };
    if (minor.checked && adultAppearance.value.trim() !== '') return { error: '未成年人设不能填写成年外观描述，请先清空' };
    const persona = JSON.parse(JSON.stringify(base));
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
    persona.gender = gender.value;
    persona.description = description.value;
    Object.assign(persona, look.fields, face.fields);
    persona.speaking_style = speakingStyle.value;
    persona.minor = minor.checked;
    persona.adult = adult.checked;
    persona.content_rules = contentRules.value;
    persona.adult_appearance = adultAppearance.value;
    persona.memory_rules = memoryRules.value;
    persona.examples = [...examples.children].map((row) => {
      const ex = exampleInputs.get(row);
      return { user: ex.user.value, reply: ex.reply.value };
    });
    if (side.value) persona.intimacy = side.value;
    else delete persona.intimacy;
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
    field('角色名', characterName, '可选，最多 20 字；设置后就是 bot 在会话里的名字'),
    field('性别', gender, '可选；选了女或男时，系统提示里写一行“性别：女。”或“性别：男。”')),
  field('描述', description, '可选，最多 2000 字'),
  field('说话风格', speakingStyle, '可选，最多 1000 字'),
  h('h3', null, '外观'),
  appearance.el,
  avatar ? [h('h3', null, '头像'), avatar.el] : null,
  h('h3', null, '风格（0–1）'),
  h('div', { class: 'form-grid' }, STYLE.map(([k, title]) => field(`${title}（${k}）`, styleInputs[k]))),
  h('h3', null, '行为'),
  h('div', { class: 'form-grid' },
    field('回复长度', replyLength),
    field('追问', followup),
    field('技术解释', technical),
    h('label', { class: 'field inline' }, memes, '允许玩梗')),
  h('h3', null, '内容尺度'),
  h('p', { class: 'hint' }, '只有成年、非未成年的人设在私聊里是开放模式，下面的内容规则、成年外观描述和记忆规则只在开放模式生效；其余情况都是保护模式，使用内置的保护规则，不能通过配置去掉。'),
  h('div', { class: 'form-grid' },
    h('label', { class: 'field inline' }, minor, '未成年角色'),
    h('label', { class: 'field inline' }, adult, '成年人设', adultNote)),
  field('内容规则（仅成年人设、私聊生效）', contentRules, '可选，最多 2000 字；开放模式下加入回复提示词，为空则不加'),
  field('成年外观描述（仅成年人设、私聊生效）', adultAppearance, '可选，最多 1000 字；开放模式下接在外貌描述后加入回复提示词，为空则不加'),
  field('记忆规则（仅成年人设、私聊生效）', memoryRules, '可选，最多 1000 字；开放模式下替换记忆整理的第 6 条，为空则省略该条'),
  h('h3', null, '亲密面'),
  intimacy.el,
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
