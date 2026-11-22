// Formatting helpers for numbers, times, dates and enum labels.

const pad = (n) => String(n).padStart(2, '0');

// fmtTime shows an RFC 3339 time in the browser's local time; null is '—'.
export function fmtTime(v) {
  if (!v) return '—';
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return String(v);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

// fmtClock shows an RFC 3339 time briefly in the browser's local time: the
// clock time on today's date, the date on another; null is ''. A list that is
// not refreshed keeps it accurate, unlike fmtAgo's "3 分钟前".
export function fmtClock(v) {
  if (!v) return '';
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return String(v);
  const now = new Date();
  if (d.toDateString() === now.toDateString()) return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  if (d.getFullYear() === now.getFullYear()) return `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

// fmtAgo shows how long ago an RFC 3339 time was.
export function fmtAgo(v) {
  if (!v) return '—';
  const t = new Date(v).getTime();
  if (Number.isNaN(t)) return String(v);
  const sec = Math.round((Date.now() - t) / 1000);
  if (sec < 5) return '刚刚';
  if (sec < 60) return `${sec} 秒前`;
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`;
  return `${Math.floor(sec / 86400)} 天前`;
}

export function fmtInt(n) {
  if (n === null || n === undefined || n === '') return '—';
  const x = Number(n);
  return Number.isFinite(x) ? x.toLocaleString('zh-CN') : String(n);
}

// fmtCompact shortens large numbers with K, M and B.
export function fmtCompact(n) {
  const x = Number(n) || 0;
  for (const [size, unit] of [[1e9, 'B'], [1e6, 'M'], [1e3, 'K']]) {
    const r = Math.round((x / size) * 10) / 10;
    if (Math.abs(r) >= 1) return String(r) + unit;
  }
  return trim(x);
}

function trim(x) {
  return String(Math.round(x * 10) / 10);
}

export function fmtNumber(n) {
  if (n === null || n === undefined) return '—';
  const x = Number(n);
  if (!Number.isFinite(x)) return String(n);
  return Number.isInteger(x) ? x.toLocaleString('zh-CN') : x.toLocaleString('zh-CN', { maximumFractionDigits: 4 });
}

export function fmtMs(ms) {
  if (ms === null || ms === undefined) return '—';
  const x = Number(ms);
  return x < 1000 ? `${Math.round(x)} ms` : `${(x / 1000).toFixed(2)} s`;
}

// dayIn is date's calendar day (YYYY-MM-DD) in the IANA zone tz, or in the
// browser's zone when tz is empty or unknown.
export function dayIn(tz, date = new Date()) {
  try {
    if (tz) return new Intl.DateTimeFormat('en-CA', { timeZone: tz, year: 'numeric', month: '2-digit', day: '2-digit' }).format(date);
  } catch {
    // fall through to the local day
  }
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

// addDays shifts a YYYY-MM-DD day by n days.
export function addDays(day, n) {
  const [y, m, d] = day.split('-').map(Number);
  return new Date(Date.UTC(y, m - 1, d + n)).toISOString().slice(0, 10);
}

export function isDay(v) {
  return /^\d{4}-\d{2}-\d{2}$/.test(v || '');
}

export function pretty(v) {
  return JSON.stringify(v, null, 2);
}

// SCENE_PLACE_MAX is how many characters a scene's place may have
// (conloomapi.ScenePlaceMaxRunes).
export const SCENE_PLACE_MAX = 16;

// sceneLabel words a chat's scene: '面对面 · 食堂', '面对面' without a place,
// '线上', also for no scene (a group's).
export function sceneLabel(scene) {
  if (!scene || scene.mode !== 'in_person') return '线上';
  return scene.place ? `面对面 · ${scene.place}` : '面对面';
}

// BAR_DEFAULT_MAX_STEP, BAR_DEFAULT_MAX_DAY and BAR_MAX_LIMIT are
// conloomapi.DefaultBarMaxStep, DefaultBarMaxDay and MaxBarLimit: the most a
// persona bar moves in one adjust_buff call and in a day when it sets no
// limit, and the highest limit it may set.
export const BAR_DEFAULT_MAX_STEP = 10;
export const BAR_DEFAULT_MAX_DAY = 30;
export const BAR_MAX_LIMIT = 100;

// barLimit reads a bar limit input: undefined when blank (the default), the
// integer 1–BAR_MAX_LIMIT, else null.
export function barLimit(text) {
  const s = String(text ?? '').trim();
  if (s === '') return undefined;
  if (!/^\d+$/.test(s)) return null;
  const n = Number(s);
  return n >= 1 && n <= BAR_MAX_LIMIT ? n : null;
}

// barLimitsText words a bar's limits when it sets either, the other one at
// its default: '单次最多 ±40，每天最多 ±30'; else ''.
export function barLimitsText(b) {
  if (!b || (!(b.max_step > 0) && !(b.max_day > 0))) return '';
  const step = b.max_step > 0 ? b.max_step : BAR_DEFAULT_MAX_STEP;
  const day = b.max_day > 0 ? b.max_day : BAR_DEFAULT_MAX_DAY;
  return `单次最多 ±${step}，每天最多 ±${day}`;
}

// actionParts splits text into its runs in order, each {text, action}, action
// true for a short action or look written in full-width brackets on one line,
// such as （笑）; a bracket left open stays plain text.
export function actionParts(text) {
  const s = text || '';
  const out = [];
  let at = 0;
  for (const m of s.matchAll(/（[^（）\n]*）/g)) {
    if (m.index > at) out.push({ text: s.slice(at, m.index), action: false });
    out.push({ text: m[0], action: true });
    at = m.index + m[0].length;
  }
  if (at < s.length) out.push({ text: s.slice(at), action: false });
  return out;
}

// previewText is message m on one line of a chat list: its text, after
// '[图片]' and a space when it carries images, or '[图片]' alone; no message
// is ''.
export function previewText(m) {
  if (!m) return '';
  if (!(m.images && m.images.length)) return m.text || '';
  return m.text ? `[图片] ${m.text}` : '[图片]';
}

export function label(map, v) {
  return Object.prototype.hasOwnProperty.call(map, v) ? map[v] : (v || '—');
}

export const TASK_STATUS = {
  queued: '排队中',
  running: '运行中',
  succeeded: '成功',
  failed: '失败',
  cancelled: '已取消',
};

export const PURPOSE = {
  chat: '对话',
  memory: '记忆整理',
  embed_index: '向量索引',
  embed_query: '向量查询',
  vision: '识图',
  suggest: '建议回复',
  state: '状态更新',
};

export const OUTCOME = {
  ok: '成功',
  error: '失败',
  timeout: '超时',
  canceled: '已取消',
};

export const CONV_TYPE = {
  group: '群聊',
  private: '私聊',
};

// PRIVATE_LEVEL are the levels of proactive.private.level, how often the bot
// starts a private chat.
export const PRIVATE_LEVEL = {
  off: '关',
  low: '低',
  mid: '中',
  high: '高',
};
