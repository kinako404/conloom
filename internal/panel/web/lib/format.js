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
// '线上'.
export function sceneLabel(scene) {
  if (scene.mode !== 'in_person') return '线上';
  return scene.place ? `面对面 · ${scene.place}` : '面对面';
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
