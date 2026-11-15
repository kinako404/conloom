import { h, mount, errorBox, loading, empty, table, kv, field, paged, select } from '../lib/dom.js';
import { get, post, put, del, convPath, isAbort } from '../lib/api.js';
import { go, href, withQuery } from '../lib/router.js';
import { fmtTime, fmtInt, pretty, label, CONV_TYPE, PRIVATE_LEVEL } from '../lib/format.js';
import { statusBadges, personaLabel } from './conversations.js';
import { taskList } from './tasks.js';
import { autonomyTab } from './autonomy.js';
import { personaForm } from './personaform.js';

const TABS = [
  ['messages', '消息'],
  ['policy', '策略'],
  ['persona', '人设'],
  ['memory', '记忆'],
  ['autonomy', '自主'],
  ['tasks', '任务'],
];

// PRIVATE_TABS need a private conversation: the hidden state.
const PRIVATE_TABS = new Set(['autonomy']);

// flash is a success message shown once after a write reloads the page.
let flash = '';

// write runs a change, showing its error in box, and reloads the page with
// done as the success message.
async function write(box, button, fn, done) {
  if (button) button.disabled = true;
  mount(box);
  try {
    await fn();
    flash = done;
    go(location.hash);
  } catch (err) {
    if (!isAbort(err)) mount(box, errorBox(err));
  } finally {
    if (button) button.disabled = false;
  }
}

function fail(box, message) {
  mount(box, errorBox({ message }));
}

function jsonDetails(v) {
  return h('details', null, h('summary', null, '查看'), h('pre', { class: 'code' }, pretty(v)));
}

// ---- messages ----

function messageItem(m) {
  return h('li', { class: m.from_bot ? 'bot' : null },
    h('div', { class: 'meta' },
      h('span', null, fmtTime(m.at)),
      h('strong', null, m.sender_name || m.sender_id),
      h('span', { class: 'mono' }, m.sender_id),
      m.from_bot ? h('span', { class: 'badge' }, 'Bot') : null,
      m.channel === 'panel' ? h('span', { class: 'badge' }, '面板') : null,
      h('span', { class: 'mono' }, `#${m.seq}`),
      m.reply_to ? h('span', null, `回复 ${m.reply_to}`) : null),
    h('div', { class: 'body' }, m.text));
}

function messagesTab({ key, query, signal }) {
  const base = href('conversations', key, 'messages');
  const search = { q: query.get('q') || '', from: query.get('from') || '', to: query.get('to') || '' };
  const q = h('input', { type: 'search', value: search.q, placeholder: '任一词命中' });
  const from = h('input', { type: 'date', value: search.from });
  const to = h('input', { type: 'date', value: search.to });
  const form = h('form', {
    class: 'toolbar',
    onsubmit: (e) => {
      e.preventDefault();
      go(withQuery(base, { q: q.value.trim(), from: from.value, to: to.value }));
    },
  }, field('关键词', q), field('从', from), field('到', to), h('button', { type: 'submit' }, '检索'),
  search.q ? h('a', { href: base }, '返回浏览') : null);

  const list = h('ul', { class: 'messages' });
  const add = (rows) => {
    for (const m of rows) list.appendChild(messageItem(m));
  };
  const path = convPath(key, 'messages');
  const view = search.q
    ? paged({
      list, add, emptyText: '没有找到消息', moreText: '更多结果',
      load: async (offset) => {
        const res = await get(path, { ...search, offset, limit: 50 }, signal);
        return { rows: res.messages || [], next: res.next_offset };
      },
    })
    : paged({
      list, add, emptyText: '还没有消息', moreText: '更早的消息',
      load: async (before) => {
        const res = await get(path, { before, limit: 50 }, signal);
        return { rows: res.messages || [], next: res.next_before };
      },
    });
  return [
    form,
    h('p', { class: 'hint' }, search.q
      ? '检索结果按相关度排序，同分时新的在前。'
      : '最新的消息在前。日期范围只用于检索。'),
    view,
  ];
}

// ---- policy ----

// PRIVATE_LEVEL_CHOICES are proactive.private.level's choices; empty is mid.
const PRIVATE_LEVEL_CHOICES = { '': '留空（按中）', ...PRIVATE_LEVEL };

// CLOCK is the HH:MM form of quiet hours.
const CLOCK = /^\d{2}:\d{2}$/;

async function policyTab({ key, conv, signal }) {
  const res = await get(convPath(key, 'policy'), null, signal);
  const cur = res.current;
  if (!cur) return [empty('尚无策略')];
  const p = cur.policy || {};

  const pauseMsg = h('div');
  const pause = h('input', { type: 'checkbox', checked: (p.concurrency || {}).max_interactive_requests === 0 });
  pause.addEventListener('change', async () => {
    const want = pause.checked;
    const question = want
      ? '暂停后，本会话正在生成和排队的回复会被取消。确定暂停？'
      : '确定恢复回复？';
    if (!confirm(question)) {
      pause.checked = !want;
      return;
    }
    pause.disabled = true;
    mount(pauseMsg);
    try {
      await post(convPath(key, want ? 'pause' : 'resume'));
      flash = want ? '已暂停' : '已恢复';
      go(location.hash);
    } catch (err) {
      pause.checked = !want;
      if (!isAbort(err)) mount(pauseMsg, errorBox(err));
    } finally {
      pause.disabled = false;
    }
  });

  const proactive = h('input', { type: 'checkbox', checked: !!(p.proactive || {}).enabled });
  const memory = h('input', { type: 'checkbox', checked: !!(p.memory || {}).enabled });
  const summaryMax = h('input', { type: 'number', min: 0, max: 8000, step: 1, value: (p.memory || {}).summary_max_chars ?? 0 });
  const noteMax = h('input', { type: 'number', min: 0, max: 2000, step: 1, value: (p.memory || {}).note_max_chars ?? 0 });
  const recent = h('input', { type: 'number', min: 0, max: 500, step: 1, value: (p.context || {}).max_recent_messages ?? '' });
  const reply = p.reply || {};
  const maxParts = h('input', { type: 'number', min: 1, max: 5, step: 1, value: reply.max_parts ?? '' });
  const isPrivate = conv.type === 'private';
  const limits = res.memory_limits || {};
  const limitHint = (range, def, cur) => `${range}；0 表示用本会话类型的默认值（${def ?? '—'}），当前生效 ${cur ?? '—'}`;
  const privateWait = h('input', { value: reply.private_wait || '', placeholder: '6s' });
  const privateMaxWait = h('input', { value: reply.private_max_wait || '', placeholder: '20s' });
  const initiative = (p.proactive || {}).private || {};
  const level = select(PRIVATE_LEVEL_CHOICES, initiative.level || '');
  const quietStart = h('input', { value: initiative.quiet_start || '', placeholder: '23:00' });
  const quietEnd = h('input', { value: initiative.quiet_end || '', placeholder: '07:00' });
  const reason = h('input', { placeholder: '修改原因' });
  const msg = h('div');
  const save = h('button', { type: 'submit', class: 'primary' }, '保存');
  const form = h('form', {
    onsubmit: (e) => {
      e.preventDefault();
      const n = Number(recent.value);
      if (recent.value.trim() === '' || !Number.isInteger(n)) return fail(msg, '历史条数必须是整数');
      const parts = Number(maxParts.value);
      if (maxParts.value.trim() === '' || !Number.isInteger(parts)) return fail(msg, '分条上限必须是整数');
      const summaryChars = Number(summaryMax.value);
      if (summaryMax.value.trim() === '' || !Number.isInteger(summaryChars)) return fail(msg, '摘要字数上限必须是整数');
      const noteChars = Number(noteMax.value);
      if (noteMax.value.trim() === '' || !Number.isInteger(noteChars)) return fail(msg, '笔记字数上限必须是整数');
      const policy = structuredClone(p);
      policy.proactive = { ...policy.proactive, enabled: proactive.checked };
      policy.memory = { ...policy.memory, enabled: memory.checked, summary_max_chars: summaryChars, note_max_chars: noteChars };
      policy.context = { ...policy.context, max_recent_messages: n };
      policy.reply = { ...policy.reply, max_parts: parts };
      if (isPrivate) {
        const wait = privateWait.value.trim();
        const maxWait = privateMaxWait.value.trim();
        if (wait === '' || maxWait === '') return fail(msg, '私聊等待时间不能为空，写成 6s、500ms、1m 这样的时长');
        policy.reply.private_wait = wait;
        policy.reply.private_max_wait = maxWait;
        const start = quietStart.value.trim();
        const end = quietEnd.value.trim();
        if ((start === '') !== (end === '')) return fail(msg, '免打扰时段的开始和结束要么都填，要么都留空');
        if (start !== '' && !(CLOCK.test(start) && CLOCK.test(end))) return fail(msg, '免打扰时段写成 23:00 这样的两位数时间');
        policy.proactive.private = { ...policy.proactive.private, level: level.value, quiet_start: start, quiet_end: end };
      }
      return write(msg, save, () => put(convPath(key, 'policy'), { policy, reason: reason.value.trim() }), '策略已保存');
    },
  },
  h('div', { class: 'form-grid' },
    h('label', { class: 'field inline' }, proactive, '主动发言'),
    h('label', { class: 'field inline' }, memory, '记忆'),
    field('历史条数', recent, '每次回复带上的最近消息数，0–500'),
    field('分条上限', maxParts, '一次回复最多分成几条消息发出，1–5；1 表示不分条')),
  h('div', { class: 'form-grid' },
    field('摘要字数上限', summaryMax, limitHint('记忆摘要最多多少字，200–8000', limits.default_summary_max_chars, limits.summary_max_chars)),
    field('笔记字数上限', noteMax, limitHint('每人的记忆笔记最多多少字，50–2000', limits.default_note_max_chars, limits.note_max_chars))),
  isPrivate ? h('div', { class: 'form-grid' },
    field('私聊等待', privateWait, '对方停顿多久才开始回复，0–60s'),
    field('私聊最长等待', privateMaxWait, '从开始等待起最多等多久，打断重来时重新计算，私聊等待–2m；两项都填 0 表示用默认值')) : null,
  isPrivate ? h('div', { class: 'form-grid' },
    field('主动找人档位', level, '关：不主动找人；低、中、高：每天最多发起 1、2、4 条，距上一条消息至少 8、4、2 小时。要打开“主动发言”才会发'),
    field('免打扰开始', quietStart, 'HH:MM，按服务器时区，可以跨午夜'),
    field('免打扰结束', quietEnd, '两项都留空时按对方的作息推算，消息不够时用 00:30–08:00')) : null,
  field('原因', reason),
  msg, save);

  const json = h('textarea', { class: 'json', spellcheck: 'false', value: pretty(p) });
  const jsonReason = h('input', { placeholder: '修改原因' });
  const jsonMsg = h('div');
  const jsonSave = h('button', { type: 'submit', class: 'primary' }, '保存 JSON');
  const jsonForm = h('form', {
    onsubmit: (e) => {
      e.preventDefault();
      let policy;
      try {
        policy = JSON.parse(json.value);
      } catch (err) {
        return fail(jsonMsg, 'JSON 格式错误：' + err.message);
      }
      if (!policy || typeof policy !== 'object' || Array.isArray(policy)) return fail(jsonMsg, '策略必须是一个 JSON 对象');
      return write(jsonMsg, jsonSave, () => put(convPath(key, 'policy'), { policy, reason: jsonReason.value.trim() }), '策略已保存');
    },
  },
  h('p', { class: 'hint' }, '整份替换当前策略。含 proactive 或 memory 段时，该段必须完整；不含时保留当前设置。'),
  json, field('原因', jsonReason), jsonMsg, jsonSave);

  const verMsg = h('div');
  const restore = async (version, button) => {
    const why = prompt(`把第 ${version} 版的策略作为新版本写入。原因：`, `恢复到第 ${version} 版`);
    if (why === null) return;
    await write(verMsg, button, () => post(convPath(key, 'policy', 'restore'), { version, reason: why }), `已恢复第 ${version} 版`);
  };
  const versions = res.versions || [];

  return [
    h('section', { class: 'card' },
      h('h2', null, `当前策略：第 ${cur.version} 版`),
      kv([['修改人', cur.updated_by || '—'], ['原因', cur.reason || '—'], ['时间', fmtTime(cur.created_at)]]),
      h('h3', null, '暂停'),
      h('label', { class: 'field inline' }, pause, '暂停回复（把并发上限设为 0）'),
      pauseMsg),
    h('section', { class: 'card' }, h('h2', null, '常用设置'), form),
    h('details', { class: 'card' }, h('summary', null, '高级：JSON 编辑'), jsonForm),
    h('section', { class: 'card' },
      h('h2', null, '历史版本'),
      verMsg,
      versions.length ? table([
        { title: '版本', class: 'nowrap', cell: (v) => `第 ${v.version} 版${v.version === cur.version ? '（当前）' : ''}` },
        { title: '时间', class: 'nowrap', cell: (v) => fmtTime(v.created_at) },
        { title: '修改人', cell: (v) => v.updated_by },
        { title: '原因', class: 'text', cell: (v) => v.reason || '—' },
        { title: '内容', cell: (v) => jsonDetails(v.policy) },
        {
          title: '',
          cell: (v) => (v.version === cur.version ? '' : h('button', {
            type: 'button',
            onclick: (e) => restore(v.version, e.currentTarget),
          }, '恢复此版本')),
        },
      ], versions) : empty('没有历史版本')),
  ];
}

// ---- persona ----

const CONTENT_MODE = { open: '开放（open）', protected: '保护（protected）' };

async function personaTab({ key, signal }) {
  const res = await get(convPath(key, 'persona'), null, signal);
  const cur = res.current;
  const templates = res.templates || [];
  const versions = res.versions || [];

  const tplMsg = h('div');
  const tpl = h('select', null, templates.map((t) => h('option', {
    value: t,
    selected: t === (cur ? cur.template : res.pending_template),
  }, t)));
  const tplReason = h('input', { placeholder: '原因' });
  const tplApply = h('button', { type: 'submit' }, '套用模板');
  const tplForm = h('form', {
    class: 'toolbar',
    onsubmit: (e) => {
      e.preventDefault();
      if (!confirm(`用模板 ${tpl.value} 的内容替换当前人设？`)) return undefined;
      return write(tplMsg, tplApply, () => put(convPath(key, 'persona'), { template: tpl.value, reason: tplReason.value.trim() }),
        `已套用模板 ${tpl.value}`);
    },
  }, field('模板', tpl), field('原因', tplReason), tplApply);

  const form = personaForm(cur ? cur.persona : res.template_doc, {
    save: (persona, reason, msg, button) => write(msg, button, () => put(convPath(key, 'persona'), { persona, reason }), '人设已保存'),
  });

  const verMsg = h('div');
  const rollback = async (version, button) => {
    const why = prompt(`用第 ${version} 版的内容新建一个版本。原因：`, `回滚到第 ${version} 版`);
    if (why === null) return;
    await write(verMsg, button, () => put(convPath(key, 'persona'), { rollback_to: version, reason: why }), `已回滚到第 ${version} 版`);
  };

  return [
    h('section', { class: 'card' },
      cur
        ? [h('h2', null, `当前人设：第 ${cur.version} 版`),
          kv([['模板', cur.template || '—'], ['修改人', cur.updated_by || '—'], ['原因', cur.reason || '—'], ['时间', fmtTime(cur.created_at)],
            ['私聊内容模式', label(CONTENT_MODE, res.content_mode)]])]
        : [h('h2', null, '尚未生成人设'),
          h('p', null, `将使用模板 ${res.pending_template || 'default'}。下面是该模板的内容，保存后写入第 1 版。`),
          kv([['私聊内容模式', label(CONTENT_MODE, res.content_mode)]])],
      templates.length ? [h('h3', null, '套用模板'), tplForm, tplMsg,
        h('p', { class: 'hint' }, '模板在', h('a', { href: '#/templates' }, '人设模板'), '页编辑；改模板不会改动这里，要用新内容就在这里再套用一次。')] : null),
    h('section', { class: 'card' }, h('h2', null, '编辑人设'), form.el),
    h('section', { class: 'card' },
      h('h2', null, '历史版本'),
      verMsg,
      versions.length ? table([
        { title: '版本', class: 'nowrap', cell: (v) => `第 ${v.version} 版${cur && v.version === cur.version ? '（当前）' : ''}` },
        { title: '模板', cell: (v) => v.template || '—' },
        { title: '时间', class: 'nowrap', cell: (v) => fmtTime(v.created_at) },
        { title: '修改人', cell: (v) => v.updated_by },
        { title: '原因', class: 'text', cell: (v) => v.reason || '—' },
        { title: '内容', cell: (v) => jsonDetails(v.persona) },
        {
          title: '',
          cell: (v) => (cur && v.version === cur.version ? '' : h('button', {
            type: 'button',
            onclick: (e) => rollback(v.version, e.currentTarget),
          }, '回滚到此版本')),
        },
      ], versions) : empty('没有历史版本')),
  ];
}

// ---- memory ----

function revisionsTable(revisions) {
  if (!revisions.length) return empty('没有修订记录');
  return table([
    { title: '时间', class: 'nowrap', cell: (r) => fmtTime(r.created_at) },
    { title: '字段', cell: (r) => r.field },
    { title: '旧值', class: 'text', cell: (r) => r.old },
    { title: '新值', class: 'text', cell: (r) => r.new },
    { title: '来源', cell: (r) => r.source },
    { title: '原因', class: 'text', cell: (r) => r.reason || '—' },
  ], revisions);
}

// INITIAL_AFFINITY is memory.InitialAffinity (internal/memory/affinity.go),
// the value a member's affinity starts from.
const INITIAL_AFFINITY = 20;

// AFFINITY_QUICK are the quick affinity buttons: a label and the new value
// computed from the current one.
const AFFINITY_QUICK = [
  ['-5', (v) => v - 5],
  ['+5', (v) => v + 5],
  ['+10', (v) => v + 10],
  ['设为 100', () => 100],
];

function memberEditor(key, mem, noteMax) {
  const note = h('textarea', { value: mem.note || '' });
  const affinity = h('input', { type: 'number', min: 0, max: 100, step: 1, value: mem.affinity ?? '' });
  const reason = h('input', { placeholder: '修改原因' });
  const msg = h('div');
  const save = h('button', { type: 'submit', class: 'primary' }, '保存');
  const memberPath = convPath(key, 'memory', 'members', encodeURIComponent(mem.user_id));
  const quick = AFFINITY_QUICK.map(([text, next]) => h('button', {
    type: 'button',
    onclick: async () => {
      const v = Math.min(Math.max(next(mem.affinity ?? INITIAL_AFFINITY), 0), 100);
      if (v === mem.affinity) {
        mount(msg, h('div', { class: 'notice' }, `好感度已经是 ${v}`));
        return;
      }
      const dirty = note.value !== (mem.note || '') || affinity.value.trim() !== String(mem.affinity ?? '');
      if (dirty && !confirm('备注或好感度有未保存的修改，快捷按钮只保存好感度，这些修改会丢失。继续吗？')) return;
      for (const b of quick) b.disabled = true;
      try {
        await write(msg, null,
          () => put(memberPath, { affinity: v, reason: reason.value.trim() || `快捷调整 ${text}` }),
          `已把 ${mem.name || mem.user_id} 的好感度调整为 ${v}`);
      } finally {
        for (const b of quick) b.disabled = false;
      }
    },
  }, text));
  return h('form', {
    class: 'card',
    onsubmit: (e) => {
      e.preventDefault();
      const body = { reason: reason.value.trim() };
      if (note.value !== (mem.note || '')) body.note = note.value;
      const a = affinity.value.trim();
      if (a === '' && mem.affinity !== null && mem.affinity !== undefined) {
        return fail(msg, '好感度不能清空，只能改成 0–100 的整数');
      }
      if (a !== '' && Number(a) !== mem.affinity) body.affinity = Number(a);
      if (!('note' in body) && !('affinity' in body)) return fail(msg, '没有修改');
      return write(msg, save, () => put(memberPath, body), `已更新 ${mem.name || mem.user_id}`);
    },
  },
  h('h3', null, `编辑 ${mem.name || ''}（${mem.user_id}）`),
  field('备注', note, noteMax ? `最多 ${noteMax} 字；写空清除备注` : '写空清除备注'),
  field('好感度', affinity, '0–100 的整数'),
  h('div', { class: 'quick-adjust', role: 'group', 'aria-label': '快捷调整好感度' }, quick,
    h('span', { class: 'hint' },
      `按已保存的值${mem.affinity == null ? `（未设置时从 ${INITIAL_AFFINITY} 起算）` : ''}调整并立即保存，只改好感度`)),
  field('原因', reason),
  msg,
  h('div', { class: 'toolbar' }, save, h('button', { type: 'button', onclick: (e) => e.currentTarget.form.remove() }, '取消')));
}

async function memoryTab({ key, signal }) {
  const m = await get(convPath(key, 'memory'), null, signal);
  const members = m.members || [];
  const msg = h('div');
  const editBox = h('div');
  const revBox = h('div');

  const consolidate = h('button', { type: 'button' }, '立即整理');
  consolidate.addEventListener('click', async () => {
    consolidate.disabled = true;
    mount(msg);
    try {
      await post(convPath(key, 'memory', 'consolidate'));
      mount(msg, h('div', { class: 'notice' }, '已提交整理，整理在后台进行，稍后刷新查看结果。'));
    } catch (err) {
      if (!isAbort(err)) mount(msg, errorBox(err));
    } finally {
      consolidate.disabled = false;
    }
  });

  const showRevisions = async (userId, title) => {
    mount(revBox, h('section', { class: 'card' }, h('h2', null, title), loading()));
    revBox.scrollIntoView({ block: 'nearest' });
    try {
      const res = await get(convPath(key, 'memory', 'revisions'), { user_id: userId }, signal);
      mount(revBox, h('section', { class: 'card' }, h('h2', null, title), revisionsTable(res.revisions || [])));
    } catch (err) {
      if (!isAbort(err)) mount(revBox, errorBox(err));
    }
  };

  const forget = async (mem, button) => {
    if (!confirm(`忘记 ${mem.name || ''}（${mem.user_id}）？这会清空备注和好感度，并删除他的修订记录。`)) return;
    await write(msg, button, () => del(convPath(key, 'memory', 'members', encodeURIComponent(mem.user_id))),
      `已忘记 ${mem.name || mem.user_id}`);
  };

  return [
    h('section', { class: 'card' },
      h('h2', null, '记忆状态'),
      kv([
        ['记忆', m.enabled
          ? h('span', { class: 'badge on' }, '生效中')
          : h('span', { class: 'badge off' }, '未生效（下面是冻结的数据）')],
        ['已整理到', `#${m.cursor_seq ?? 0}`],
        ['上次整理', fmtTime(m.last_run_at)],
        m.last_error ? ['最近错误', h('span', { class: 'pre' }, m.last_error)] : null,
      ]),
      h('div', { class: 'toolbar' }, consolidate),
      msg),
    h('section', { class: 'card' },
      h('h2', null, '摘要'),
      m.summary ? h('div', { class: 'pre' }, m.summary) : empty('还没有摘要'),
      h('div', { class: 'toolbar' },
        h('button', { type: 'button', onclick: () => showRevisions('', '摘要修订记录') }, '修订记录'))),
    h('section', { class: 'card' },
      h('h2', null, `群友记忆（${members.length}）`),
      members.length ? table([
        { title: '群友', cell: (x) => [h('div', null, x.name || '—'), h('div', { class: 'mono muted' }, x.user_id)] },
        { title: '备注', class: 'text', cell: (x) => x.note || '—' },
        { title: '好感度', class: 'num', cell: (x) => (x.affinity === null || x.affinity === undefined ? '—' : `${x.affinity}${x.tier ? `（${x.tier}）` : ''}`) },
        { title: '更新时间', class: 'nowrap', cell: (x) => fmtTime(x.updated_at) },
        {
          title: '操作',
          cell: (x) => h('div', { class: 'toolbar' },
            h('button', {
              type: 'button',
              onclick: () => {
                mount(editBox, memberEditor(key, x, m.note_max_chars));
                editBox.scrollIntoView({ block: 'nearest' });
              },
            }, '编辑'),
            h('button', { type: 'button', onclick: () => showRevisions(x.user_id, `修订记录：${x.name || ''}（${x.user_id}）`) }, '修订记录'),
            h('button', { type: 'button', class: 'danger', onclick: (e) => forget(x, e.currentTarget) }, '忘记')),
        },
      ], members) : empty('还没有群友记忆'),
      editBox),
    revBox,
  ];
}

// ---- tasks ----

function tasksTab({ key, signal }) {
  return [taskList(signal, { conversation: key })];
}

const TAB_VIEWS = {
  messages: messagesTab,
  policy: policyTab,
  persona: personaTab,
  memory: memoryTab,
  autonomy: (ctx) => autonomyTab({ ...ctx, write }),
  tasks: tasksTab,
};

// PANEL is the platform of conversations held in the panel (store.PlatformPanel).
const PANEL = 'panel';

// tabsOf are the conversation's tabs, PRIVATE_TABS only in a private one.
function tabsOf(c) {
  return TABS.filter(([t]) => !PRIVATE_TABS.has(t) || c.type === 'private');
}

// deleteChat is the button that deletes a panel chat after confirmation, and
// the box its error shows in.
function deleteChat(c) {
  const msg = h('div');
  const button = h('button', {
    type: 'button',
    class: 'danger',
    onclick: async () => {
      if (!confirm('删除这个聊天？聊天记录、记忆、人设和策略都会删除，不能恢复；用量统计和审计记录保留。')) return;
      button.disabled = true;
      mount(msg);
      try {
        await del(convPath(c.key));
        go('#/conversations');
      } catch (err) {
        if (!isAbort(err)) mount(msg, errorBox(err));
      } finally {
        button.disabled = false;
      }
    },
  }, '删除这个聊天');
  return { button, msg };
}

function header(c) {
  const open = c.type === 'private' ? h('a', { class: 'button', href: href('chat', c.key) }, '在聊天页打开') : null;
  const removal = c.platform === PANEL ? deleteChat(c) : null;
  return [
    h('p', null, h('a', { href: '#/conversations' }, '← 会话列表')),
    h('h1', null, `${label(CONV_TYPE, c.type)} ${c.platform_conversation_id}`),
    h('section', { class: 'card' }, kv([
      ['会话键', h('span', { class: 'mono' }, c.key)],
      ['Bot', `${c.bot_name || ''}（${c.bot_instance_id}）`],
      ['平台', c.platform],
      ['消息数', fmtInt(c.message_count)],
      ['最后消息', fmtTime(c.last_message_at)],
      ['状态', statusBadges(c)],
      ['人设', personaLabel(c)],
    ]),
    open || removal ? h('div', { class: 'toolbar' }, open, removal && removal.button) : null,
    removal && removal.msg),
  ];
}

// OLD_CHAT_TAB was a private conversation's chat tab, now the chat page; its
// links, such as bookmarks, open the chat there.
const OLD_CHAT_TAB = 'chat';

export async function conversationView(ctx) {
  const key = ctx.params.key;
  if (ctx.params.tab === OLD_CHAT_TAB) {
    location.replace(href('chat', key));
    return;
  }
  const conv = await get(convPath(key), null, ctx.signal);
  const tabs = tabsOf(conv);
  const tab = tabs.some(([t]) => t === ctx.params.tab) ? ctx.params.tab : 'messages';
  const note = flash ? h('div', { class: 'notice' }, flash) : null;
  flash = '';
  const body = h('div', null, loading());
  ctx.show(
    header(conv),
    h('nav', { class: 'tabs' }, tabs.map(([t, title]) => h('a', {
      href: href('conversations', key, t),
      class: t === tab ? 'active' : null,
    }, title))),
    note,
    body,
  );
  try {
    const nodes = await TAB_VIEWS[tab]({ ...ctx, key, conv });
    if (!ctx.signal.aborted) mount(body, nodes);
  } catch (err) {
    if (!isAbort(err) && !ctx.signal.aborted) mount(body, errorBox(err));
  }
}
