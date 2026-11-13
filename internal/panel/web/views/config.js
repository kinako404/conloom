import { h, mount, empty, table } from '../lib/dom.js';
import { get } from '../lib/api.js';

const SET = '(已设置)';
const UNSET = '(未设置)';

// flatten lists the leaves of a nested value as {path, value}, with array
// items as path[i]; empty objects and arrays are leaves.
function flatten(v, path, out) {
  if (Array.isArray(v)) {
    if (!v.length) out.push({ path, value: v });
    v.forEach((x, i) => flatten(x, `${path}[${i}]`, out));
  } else if (v !== null && typeof v === 'object') {
    const keys = Object.keys(v);
    if (!keys.length) out.push({ path, value: v });
    for (const k of keys) flatten(v[k], path ? `${path}.${k}` : k, out);
  } else {
    out.push({ path, value: v });
  }
  return out;
}

function valueNode(v) {
  if (v === SET) return h('span', { class: 'badge on' }, '已设置');
  if (v === UNSET) return h('span', { class: 'badge' }, '未设置');
  if (v === null) return h('span', { class: 'muted' }, 'null');
  if (typeof v === 'string') return v === '' ? h('span', { class: 'muted' }, '""') : v;
  if (typeof v === 'object') return h('span', { class: 'muted' }, JSON.stringify(v));
  return String(v);
}

export async function configView(ctx) {
  const res = await get('config', null, ctx.signal);
  const rows = flatten(res.config || {}, '', []);
  const filter = h('input', { type: 'search', placeholder: '按配置项筛选' });
  const list = h('div');
  const show = () => {
    const needle = filter.value.trim().toLowerCase();
    const shown = needle ? rows.filter((r) => r.path.toLowerCase().includes(needle)) : rows;
    mount(list, shown.length ? table([
      { title: '配置项', cell: (r) => h('span', { class: 'mono' }, r.path) },
      { title: '值', class: 'text', cell: (r) => valueNode(r.value) },
    ], shown) : empty('没有匹配的配置项'));
  };
  filter.addEventListener('input', show);
  show();
  ctx.show(
    h('h1', null, '配置'),
    h('p', { class: 'hint' }, '当前生效的配置，只读。密钥类字段只显示是否已设置；修改配置需要改文件后重启。'),
    h('div', { class: 'toolbar' }, filter),
    list,
  );
}
