import { h } from '../lib/dom.js';

// imageURL is the address of the reply copy of the persona image id, the copy
// the reply model sees. v gives every image its own address, so the browser
// never shows an earlier image in its place.
export function imageURL(id) {
  const v = encodeURIComponent(id);
  return `api/persona-images/${v}?size=reply&v=${v}`;
}

// originalImageURL is the address of the persona image id as it was
// uploaded, on this panel's origin, so a canvas it is drawn on stays
// readable.
export function originalImageURL(id) {
  const v = encodeURIComponent(id);
  return `api/persona-images/${v}?size=original&v=${v}`;
}

// COLORS is how many background colors style.css has for a bot without an
// image, as .avatar-c0 to .avatar-c7.
const COLORS = 8;

// colorOf picks a bot's background color by a simple hash of its ID, so a
// bot keeps its color.
function colorOf(id) {
  let n = 0;
  for (const c of String(id || '')) n = (n * 31 + c.codePointAt(0)) % 1000003;
  return n % COLORS;
}

// letterAvatar is a colored circle with the first character of the bot's name.
function letterAvatar(bot) {
  const name = bot.name || bot.id || '?';
  return h('span', { class: `avatar avatar-c${colorOf(bot.id)}`, 'aria-hidden': 'true' }, [...name][0]);
}

// avatarEl is the round avatar of bot ({id, name, avatar}), size pixels
// across: its persona image cropped to a circle when it has one, falling back
// to its first letter when the image fails to load, otherwise the letter.
export function avatarEl(bot, size) {
  let el;
  if (bot.avatar) {
    el = h('img', { class: 'avatar', src: imageURL(bot.avatar), alt: '', width: size, height: size, loading: 'lazy' });
    el.addEventListener('error', () => el.replaceWith(avatarEl({ ...bot, avatar: '' }, size)), { once: true });
  } else {
    el = letterAvatar(bot);
  }
  el.style.setProperty('--avatar-size', `${size}px`);
  return el;
}

// onlineDot is the green dot of an avatar whose bot is online, 在线 to screen
// readers and on hover; it is there either way and hidden while the bot is
// not online, so that a poll can switch it without rebuilding the avatar.
export function onlineDot(online) {
  return h('span', { class: 'online-dot', role: 'img', 'aria-label': '在线', title: '在线', hidden: !online });
}

// withDot is avatar, an avatarEl, with dot at its bottom right; id, when
// given, names the pair, so that the dot can describe something else.
export function withDot(avatar, dot, id) {
  return h('span', { class: 'avatar-wrap', id }, avatar, dot);
}
