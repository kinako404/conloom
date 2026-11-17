// The settings page: the default policies of group and private
// conversations, which every conversation following them uses.

import { h, kv } from '../lib/dom.js';
import { get, post, put } from '../lib/api.js';
import { go } from '../lib/router.js';
import { fmtTime } from '../lib/format.js';
import { policyForm, policyJSONForm, policyVersions } from './policyform.js';

// KINDS are the kinds of default policy and their titles.
const KINDS = [
  ['group', '群聊默认策略'],
  ['private', '私聊默认策略'],
];

// flash is the outcome of a save shown once in its kind's block after the page
// reloads: {kind, text, warnings}.
let flash = null;

// saved stores a change of kind's default policy with fn, which answers
// {current, warnings}, and reloads the page with done and the warnings.
async function saved(kind, fn, done) {
  const res = await fn();
  flash = { kind, text: done, warnings: (res && res.warnings) || [] };
  go(location.hash);
}

function outcome(kind, shown) {
  if (!shown || shown.kind !== kind) return null;
  return [
    h('div', { class: 'notice' }, shown.text),
    shown.warnings.map((w) => h('p', { class: 'hint warn', role: 'status' }, w)),
  ];
}

function block(kind, title, section, shown) {
  const cur = section.current || {};
  const path = `settings/policies/${kind}`;
  const store = (policy, reason) => saved(kind, () => put(path, { policy, reason }), '已保存');
  return h('section', { class: 'card' },
    h('h2', null, title),
    h('p', { class: 'hint' }, `当前有 ${section.followers ?? 0} 个会话跟随这个默认值；单独改过策略的会话不受影响。`),
    outcome(kind, shown),
    h('h3', null, `当前：第 ${cur.version ?? 0} 版`),
    kv([['修改人', cur.updated_by || '—'], ['原因', cur.reason || '—'], ['时间', fmtTime(cur.created_at)]]),
    h('h3', null, '常用设置'),
    policyForm({ policy: cur.policy, isPrivate: kind === 'private', limits: section.memory_limits, forDefaults: true, onSave: store }),
    h('details', null, h('summary', null, '高级：JSON 编辑'),
      policyJSONForm({ policy: cur.policy, hint: '整份替换默认策略；没写的字段按内置默认值补齐。', onSave: store })),
    h('h3', null, '历史版本'),
    policyVersions({
      versions: section.versions || [],
      current: cur.version,
      onRestore: (version, reason) => saved(kind, () => post(`${path}/restore`, { version, reason }), `已恢复第 ${version} 版`),
    }));
}

export async function settingsView(ctx) {
  const res = await get('settings/policies', null, ctx.signal);
  const shown = flash;
  flash = null;
  ctx.show(
    h('h1', null, '设置'),
    h('p', { class: 'hint' }, '默认策略保存后立即生效，不用重启。'),
    KINDS.map(([kind, title]) => block(kind, title, res[kind] || {}, shown)),
  );
}
