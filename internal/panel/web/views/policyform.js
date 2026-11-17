// The policy forms shared by a conversation's policy tab and the settings
// page, which edits the default policies: the common settings, the JSON
// editor and the version list.

import { h, mount, errorBox, empty, table, field, select } from '../lib/dom.js';
import { isAbort } from '../lib/api.js';
import { fmtTime, pretty, PRIVATE_LEVEL } from '../lib/format.js';

// PRIVATE_LEVEL_CHOICES are proactive.private.level's choices; empty is mid.
export const PRIVATE_LEVEL_CHOICES = { '': '留空（按中）', ...PRIVATE_LEVEL };

// CLOCK is the HH:MM form of quiet hours.
export const CLOCK = /^\d{2}:\d{2}$/;

export function fail(box, message) {
  mount(box, errorBox({ message }));
}

// submit runs fn, a save, with button disabled, showing its error in box.
export async function submit(box, button, fn) {
  if (button) button.disabled = true;
  mount(box);
  try {
    await fn();
  } catch (err) {
    if (!isAbort(err)) mount(box, errorBox(err));
  } finally {
    if (button) button.disabled = false;
  }
}

export function jsonDetails(v) {
  return h('details', null, h('summary', null, '查看'), h('pre', { class: 'code' }, pretty(v)));
}

// policyForm edits the common settings of policy, keeping the fields the form
// does not show; isPrivate adds a private chat's. limits are the memory limits
// in effect (memoryLimitsView). forDefaults says policy is a default policy,
// which is stored as written: 0 and 0 for the private waits means no wait, not
// the defaults. Submitting calls onSave(policy, reason), whose promise stores
// it; its error shows in the form.
export function policyForm({ policy, isPrivate, limits, forDefaults = false, onSave }) {
  const p = policy || {};
  const proactive = h('input', { type: 'checkbox', checked: !!(p.proactive || {}).enabled });
  const memory = h('input', { type: 'checkbox', checked: !!(p.memory || {}).enabled });
  const summaryMax = h('input', { type: 'number', min: 0, max: 8000, step: 1, value: (p.memory || {}).summary_max_chars ?? 0 });
  const noteMax = h('input', { type: 'number', min: 0, max: 2000, step: 1, value: (p.memory || {}).note_max_chars ?? 0 });
  const recent = h('input', { type: 'number', min: 0, max: 500, step: 1, value: (p.context || {}).max_recent_messages ?? '' });
  const reply = p.reply || {};
  const maxParts = h('input', { type: 'number', min: 1, max: 5, step: 1, value: reply.max_parts ?? '' });
  const lim = limits || {};
  const zeroLimit = forDefaults ? '0 表示用内置默认值' : '0 表示用本会话类型的默认值';
  const limitHint = (range, def, cur) => `${range}；${zeroLimit}（${def ?? '—'}），当前生效 ${cur ?? '—'}`;
  const zeroWait = forDefaults ? '两项都填 0 表示不等待、收到消息立即回复' : '两项都填 0 表示用默认值';
  const privateWait = h('input', { value: reply.private_wait || '', placeholder: '6s' });
  const privateMaxWait = h('input', { value: reply.private_max_wait || '', placeholder: '20s' });
  const initiative = (p.proactive || {}).private || {};
  const level = select(PRIVATE_LEVEL_CHOICES, initiative.level || '');
  const quietStart = h('input', { value: initiative.quiet_start || '', placeholder: '23:00' });
  const quietEnd = h('input', { value: initiative.quiet_end || '', placeholder: '07:00' });
  const reason = h('input', { placeholder: '修改原因' });
  const msg = h('div');
  const save = h('button', { type: 'submit', class: 'primary' }, '保存');
  return h('form', {
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
      const next = JSON.parse(JSON.stringify(p));
      next.proactive = { ...next.proactive, enabled: proactive.checked };
      next.memory = { ...next.memory, enabled: memory.checked, summary_max_chars: summaryChars, note_max_chars: noteChars };
      next.context = { ...next.context, max_recent_messages: n };
      next.reply = { ...next.reply, max_parts: parts };
      if (isPrivate) {
        const wait = privateWait.value.trim();
        const maxWait = privateMaxWait.value.trim();
        if (wait === '' || maxWait === '') return fail(msg, '私聊等待时间不能为空，写成 6s、500ms、1m 这样的时长');
        next.reply.private_wait = wait;
        next.reply.private_max_wait = maxWait;
        const start = quietStart.value.trim();
        const end = quietEnd.value.trim();
        if ((start === '') !== (end === '')) return fail(msg, '免打扰时段的开始和结束要么都填，要么都留空');
        if (start !== '' && !(CLOCK.test(start) && CLOCK.test(end))) return fail(msg, '免打扰时段写成 23:00 这样的两位数时间');
        next.proactive.private = { ...next.proactive.private, level: level.value, quiet_start: start, quiet_end: end };
      }
      return submit(msg, save, () => onSave(next, reason.value.trim()));
    },
  },
  h('div', { class: 'form-grid' },
    h('label', { class: 'field inline' }, proactive, '主动发言'),
    h('label', { class: 'field inline' }, memory, '记忆'),
    field('历史条数', recent, '每次回复带上的最近消息数，0–500'),
    field('分条上限', maxParts, '一次回复最多分成几条消息发出，1–5；1 表示不分条')),
  h('div', { class: 'form-grid' },
    field('摘要字数上限', summaryMax, limitHint('记忆摘要最多多少字，200–8000', lim.default_summary_max_chars, lim.summary_max_chars)),
    field('笔记字数上限', noteMax, limitHint('每人的记忆笔记最多多少字，50–2000', lim.default_note_max_chars, lim.note_max_chars))),
  isPrivate ? h('div', { class: 'form-grid' },
    field('私聊等待', privateWait, '对方停顿多久才开始回复，0–60s'),
    field('私聊最长等待', privateMaxWait, `从开始等待起最多等多久，打断重来时重新计算，私聊等待–2m；${zeroWait}`)) : null,
  isPrivate ? h('div', { class: 'form-grid' },
    field('主动找人档位', level, '关：不主动找人；低、中、高：每天最多发起 1、2、4 条，距上一条消息至少 8、4、2 小时。要打开“主动发言”才会发'),
    field('免打扰开始', quietStart, 'HH:MM，按服务器时区，可以跨午夜'),
    field('免打扰结束', quietEnd, '两项都留空时按对方的作息推算，消息不够时用 00:30–08:00')) : null,
  field('原因', reason),
  msg, save);
}

// policyJSONForm edits policy as JSON, with hint above it. Submitting calls
// onSave(policy, reason), whose promise stores it; its error shows in the form.
export function policyJSONForm({ policy, hint, onSave }) {
  const json = h('textarea', { class: 'json', spellcheck: 'false', value: pretty(policy || {}) });
  const reason = h('input', { placeholder: '修改原因' });
  const msg = h('div');
  const save = h('button', { type: 'submit', class: 'primary' }, '保存 JSON');
  return h('form', {
    onsubmit: (e) => {
      e.preventDefault();
      let next;
      try {
        next = JSON.parse(json.value);
      } catch (err) {
        return fail(msg, 'JSON 格式错误：' + err.message);
      }
      if (!next || typeof next !== 'object' || Array.isArray(next)) return fail(msg, '策略必须是一个 JSON 对象');
      return submit(msg, save, () => onSave(next, reason.value.trim()));
    },
  },
  h('p', { class: 'hint' }, hint),
  json, field('原因', reason), msg, save);
}

// policyVersions lists versions, newest first, current being the version in
// effect; label(v) is text added to a version's name. Each other version has
// 恢复此版本, which asks for a reason and calls onRestore(version, reason),
// whose promise stores it as a new version; its error shows above the list.
export function policyVersions({ versions, current, onRestore, label = () => '' }) {
  const msg = h('div');
  const restore = (version, button) => {
    const why = prompt(`把第 ${version} 版的策略作为新版本写入。原因：`, `恢复到第 ${version} 版`);
    if (why === null) return undefined;
    return submit(msg, button, () => onRestore(version, why));
  };
  return [
    msg,
    versions.length ? table([
      { title: '版本', class: 'nowrap', cell: (v) => `第 ${v.version} 版${label(v)}${v.version === current ? '（当前）' : ''}` },
      { title: '时间', class: 'nowrap', cell: (v) => fmtTime(v.created_at) },
      { title: '修改人', cell: (v) => v.updated_by },
      { title: '原因', class: 'text', cell: (v) => v.reason || '—' },
      { title: '内容', cell: (v) => jsonDetails(v.policy) },
      {
        title: '',
        cell: (v) => (v.version === current ? '' : h('button', {
          type: 'button',
          onclick: (e) => restore(v.version, e.currentTarget),
        }, '恢复此版本')),
      },
    ], versions) : empty('没有历史版本'),
  ];
}
