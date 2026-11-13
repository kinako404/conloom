// Client for the panel's JSON API under /panel/api/. The session cookie is
// HttpOnly; the CSRF token from login or GET session lives only in this
// module, never in storage.

const BASE = 'api/';

let csrfToken = '';
let user = '';
let timezone = null;

export class ApiError extends Error {
  constructor(status, code, message, retryAfter) {
    super(message);
    this.status = status;
    this.code = code;
    this.retryAfter = retryAfter;
  }
}

export function isAbort(err) {
  return err && err.name === 'AbortError';
}

export function currentUser() {
  return user;
}

function remember(sess) {
  csrfToken = (sess && sess.csrf_token) || '';
  user = (sess && sess.user) || '';
}

function forget() {
  csrfToken = '';
  user = '';
}

// loginHash is the login route that returns to next afterwards.
export function loginHash(next) {
  if (!next || next.startsWith('#/login')) return '#/login';
  return '#/login?next=' + encodeURIComponent(next);
}

function toLogin() {
  const here = location.hash || '#/';
  if (!here.startsWith('#/login')) location.hash = loginHash(here);
}

// qs encodes params, leaving out empty values.
export function qs(params) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) {
    if (v !== undefined && v !== null && v !== '') p.set(k, String(v));
  }
  const s = p.toString();
  return s ? '?' + s : '';
}

// convPath is the API path of a conversation, or of one of its sub-resources.
export function convPath(key, ...rest) {
  return ['conversations', encodeURIComponent(key), ...rest].join('/');
}

export async function request(method, path, { body, params, signal } = {}) {
  if (method !== 'GET' && path !== 'login' && !csrfToken) await loadSession(signal);
  const headers = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken;
  let res;
  try {
    res = await fetch(BASE + path + qs(params), {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
      cache: 'no-store',
      signal,
    });
  } catch (err) {
    if (isAbort(err)) throw err;
    throw new ApiError(0, 'NETWORK', '无法连接服务器：' + err.message);
  }
  if (res.status === 401 && path !== 'login') {
    forget();
    toLogin();
    throw new ApiError(401, 'UNAUTHORIZED', '登录已失效，请重新登录');
  }
  if (res.status === 204) return null;
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }
  if (!res.ok) {
    const e = (data && data.error) || {};
    throw new ApiError(res.status, e.code || '', e.message || `请求失败（HTTP ${res.status}）`,
      res.headers.get('Retry-After'));
  }
  return data;
}

export const get = (path, params, signal) => request('GET', path, { params, signal });
export const post = (path, body) => request('POST', path, { body });
export const put = (path, body) => request('PUT', path, { body });
export const del = (path) => request('DELETE', path);

export async function loadSession(signal) {
  const sess = await request('GET', 'session', { signal });
  remember(sess);
  return sess;
}

export async function login(name, password) {
  const sess = await request('POST', 'login', { body: { name, password } });
  remember(sess);
  return sess;
}

export async function logout() {
  try {
    await request('POST', 'logout');
  } finally {
    forget();
  }
}

// noteTimezone records the server's timezone, which overview reports.
export function noteTimezone(tz) {
  if (tz) timezone = Promise.resolve(tz);
}

// serverTimezone is the IANA zone the server buckets days in; usage dates
// are days in it.
export function serverTimezone(signal) {
  if (!timezone) {
    timezone = get('overview', null, signal).then((o) => o.timezone || '');
    timezone.catch(() => { timezone = null; });
  }
  return timezone;
}
