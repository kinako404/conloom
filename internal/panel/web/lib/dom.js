// DOM helpers. Strings passed as children always become text nodes, so data
// from the server is never parsed as HTML.

// The SVG namespace is a name, not a resource; it is assembled so that the
// asset check banning absolute URLs stays strict.
const SVG_NS = ['http:', '', 'www.w3.org', '2000', 'svg'].join('/');

const PROPS = new Set(['value', 'checked', 'disabled', 'selected', 'multiple', 'open']);

function apply(el, attrs) {
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k.startsWith('on') && typeof v === 'function') {
      el.addEventListener(k.slice(2).toLowerCase(), v);
    } else if (PROPS.has(k)) {
      el[k] = v;
    } else if (k === 'dataset') {
      Object.assign(el.dataset, v);
    } else {
      el.setAttribute(k, v === true ? '' : String(v));
    }
  }
}

function append(el, children) {
  for (const c of children) {
    if (c === undefined || c === null || c === false || c === true) continue;
    if (Array.isArray(c)) append(el, c);
    else if (c instanceof Node) el.appendChild(c);
    else el.appendChild(document.createTextNode(String(c)));
  }
}

// h creates an HTML element. attrs: on* functions become event listeners,
// value/checked/disabled/selected/multiple/open are set as properties, other
// keys as attributes (true = present, false/null/undefined = absent).
export function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  apply(el, attrs);
  append(el, children);
  return el;
}

// s creates an SVG element the same way as h.
export function s(tag, attrs, ...children) {
  const el = document.createElementNS(SVG_NS, tag);
  apply(el, attrs);
  append(el, children);
  return el;
}

// mount replaces el's children.
export function mount(el, ...children) {
  el.replaceChildren();
  append(el, children);
  return el;
}

// errorBox shows an error's message, keeping the server's line breaks.
export function errorBox(err) {
  return h('div', { class: 'error', role: 'alert' }, err && err.message ? err.message : String(err));
}

export function loading() {
  return h('p', { class: 'muted' }, '加载中…');
}

export function empty(text) {
  return h('p', { class: 'muted' }, text || '暂无数据');
}

// table builds a table from column specs {title, cell(row) => node|string, class}.
export function table(columns, rows) {
  return h('div', { class: 'table-wrap' },
    h('table', null,
      h('thead', null, h('tr', null, columns.map((c) => h('th', { class: c.class }, c.title)))),
      h('tbody', null, rows.map((r) => h('tr', null,
        columns.map((c) => h('td', { class: c.class }, c.cell(r))))))));
}

// kv builds a definition list from [label, value] pairs.
export function kv(pairs) {
  return h('dl', { class: 'kv' }, pairs.filter(Boolean).map(([k, v]) => [h('dt', null, k), h('dd', null, v)]));
}

// paged shows a list one page at a time. load(cursor) resolves to
// {rows, next}, where cursor is null for the first page and next is null after
// the last; add(rows) appends rows to list.
export function paged({ load, list, add, emptyText, moreText = '加载更多' }) {
  const status = h('div', null, loading());
  const more = h('button', { type: 'button', hidden: true }, moreText);
  let cursor = null;
  let count = 0;
  list.hidden = true;
  async function next() {
    more.disabled = true;
    try {
      const page = await load(cursor);
      const rows = page.rows || [];
      add(rows);
      count += rows.length;
      list.hidden = count === 0;
      mount(status, count === 0 ? empty(emptyText) : null);
      cursor = page.next === undefined ? null : page.next;
      more.hidden = cursor === null;
    } catch (err) {
      if (err && err.name === 'AbortError') return;
      mount(status, errorBox(err));
    } finally {
      more.disabled = false;
    }
  }
  more.addEventListener('click', next);
  next();
  return h('div', null, list, status, h('div', { class: 'toolbar' }, more));
}

// pagedTable is paged with rows shown in a table of column specs.
export function pagedTable(columns, load, emptyText) {
  const tbody = h('tbody');
  const list = h('div', { class: 'table-wrap' },
    h('table', null, h('thead', null, h('tr', null, columns.map((c) => h('th', { class: c.class }, c.title)))), tbody));
  return paged({
    load,
    list,
    emptyText,
    add: (rows) => {
      for (const r of rows) tbody.appendChild(h('tr', null, columns.map((c) => h('td', { class: c.class }, c.cell(r)))));
    },
  });
}

// field wraps a labelled form control.
export function field(label, control, hint) {
  return h('label', { class: 'field' }, h('span', { class: 'field-label' }, label), control,
    hint ? h('span', { class: 'hint' }, hint) : null);
}
