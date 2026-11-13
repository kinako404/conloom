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

// fmtCompact shortens large numbers with 万 and 亿.
export function fmtCompact(n) {
  const x = Number(n) || 0;
  const a = Math.abs(x);
  if (a >= 1e8) return trim(x / 1e8) + '亿';
  if (a >= 1e4) return trim(x / 1e4) + '万';
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
