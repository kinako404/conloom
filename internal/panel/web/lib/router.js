// Hash router. Patterns look like '/conversations/:key/:tab'; parameters are
// URL-decoded. Each render gets an AbortSignal that fires when the user leaves
// the page, which cancels its requests and timers.

import { mount, errorBox } from './dom.js';
import { isAbort } from './api.js';

const routes = [];
let outlet = null;
let fallback = null;
let beforeRender = null;
let controller = null;

export function route(pattern, view) {
  routes.push({ parts: pattern.split('/').filter(Boolean), view });
}

export function parseHash(hash) {
  const raw = (hash || '').replace(/^#/, '') || '/';
  const i = raw.indexOf('?');
  const path = i < 0 ? raw : raw.slice(0, i);
  return {
    path,
    segments: path.split('/').filter(Boolean),
    query: new URLSearchParams(i < 0 ? '' : raw.slice(i + 1)),
  };
}

function match(segments) {
  for (const r of routes) {
    if (r.parts.length !== segments.length) continue;
    const params = {};
    let ok = true;
    for (let i = 0; ok && i < r.parts.length; i++) {
      const p = r.parts[i];
      if (p.startsWith(':')) {
        try {
          params[p.slice(1)] = decodeURIComponent(segments[i]);
        } catch {
          ok = false;
        }
      } else if (p !== segments[i]) {
        ok = false;
      }
    }
    if (ok) return { view: r.view, params };
  }
  return null;
}

// href builds a route from raw segments, encoding each one.
export function href(...segments) {
  return '#/' + segments.map((s) => encodeURIComponent(s)).join('/');
}

// withQuery appends params to a route, leaving out empty values.
export function withQuery(hash, params) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) {
    if (v !== undefined && v !== null && v !== '') p.set(k, String(v));
  }
  const s = p.toString();
  return s ? hash + '?' + s : hash;
}

// go navigates to hash, rendering again when it is already current.
export function go(hash) {
  if (location.hash === hash) render();
  else location.hash = hash;
}

export function render() {
  if (controller) controller.abort();
  controller = new AbortController();
  const signal = controller.signal;
  const loc = parseHash(location.hash);
  const m = match(loc.segments);
  if (beforeRender) beforeRender(loc);
  const ctx = {
    el: outlet,
    params: m ? m.params : {},
    query: loc.query,
    path: loc.path,
    signal,
    // show replaces the page unless the user has already left it.
    show: (...children) => {
      if (!signal.aborted) mount(outlet, ...children);
    },
  };
  const fail = (err) => {
    if (!isAbort(err) && !signal.aborted) mount(outlet, errorBox(err));
  };
  try {
    const out = (m ? m.view : fallback)(ctx);
    if (out && typeof out.catch === 'function') out.catch(fail);
  } catch (err) {
    fail(err);
  }
}

export function start(el, notFound, hook) {
  outlet = el;
  fallback = notFound;
  beforeRender = hook;
  window.addEventListener('hashchange', render);
  render();
}
