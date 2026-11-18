import { h, mount } from './lib/dom.js';
import { route, start, go } from './lib/router.js';
import { currentUser, loadSession, logout } from './lib/api.js';
import { SECTIONS } from './lib/sections.js';
import { loginView } from './views/login.js';
import { overviewView } from './views/overview.js';
import { usageView } from './views/usage.js';
import { chatsView } from './views/chats.js';
import { chatsClassicView } from './views/chatsclassic.js';
import { conversationsView } from './views/conversations.js';
import { conversationView } from './views/conversation.js';
import { templatesView, templateView } from './views/templates.js';
import { tasksView, taskDetailView } from './views/tasks.js';
import { auditView } from './views/audit.js';
import { settingsView } from './views/settings.js';
import { configView } from './views/config.js';

route('/', overviewView);
route('/login', loginView);
route('/usage', usageView);
route('/chat', chatsClassicView);
route('/chat/:key', chatsClassicView);
route('/im', chatsView);
route('/im/:key', chatsView);
route('/conversations', conversationsView);
route('/conversations/:key', conversationView);
route('/conversations/:key/:tab', conversationView);
route('/templates', templatesView);
route('/templates/:name', templateView);
route('/tasks', tasksView);
route('/tasks/:id', taskDetailView);
route('/audit', auditView);
route('/settings', settingsView);
route('/config', configView);

const topbar = document.getElementById('topbar');
const nav = document.getElementById('nav');
const userEl = document.getElementById('user');
const main = document.getElementById('main');

const sectionLink = ([section, title]) => {
  const a = h('a', { href: '#/' + section }, title);
  a.dataset.section = section;
  return a;
};

// The top bar shows the sections as a row of links. When the row does not fit
// on one line beside the brand and the user, the bar is collapsed: the row is
// hidden (still laid out, so its width can be measured) and a ☰ button with
// the current section's title opens the sections in a menu under the bar.
const links = SECTIONS.map(sectionLink);
const items = SECTIONS.map(sectionLink);
const row = h('div', { class: 'nav-links' }, links);
const toggle = h('button', {
  type: 'button',
  class: 'nav-toggle',
  title: '导航菜单',
  'aria-label': '导航菜单',
  'aria-expanded': 'false',
  'aria-controls': 'nav-menu',
}, '☰');
const current = h('span', { class: 'nav-current' });
const bar = h('div', { class: 'nav-bar' }, toggle, current);
const menu = h('div', { class: 'nav-menu', id: 'nav-menu', hidden: true }, items);
mount(nav, row, bar, menu);

// SLACK keeps a collapsed bar collapsed until the row fits with a few pixels
// to spare, so a width at the edge does not switch back and forth.
const SLACK = 4;
let collapsed = false;

// fit collapses the bar when the row's single-line width exceeds the room
// between the brand and the user. Neither depends on the mode, so switching
// does not change the measurement.
function fit() {
  if (topbar.hidden) return;
  const room = nav.getBoundingClientRect().width;
  if (!room) return;
  const need = links[links.length - 1].getBoundingClientRect().right - links[0].getBoundingClientRect().left;
  const next = collapsed ? need > room - SLACK : need > room;
  if (next === collapsed) return;
  collapsed = next;
  topbar.classList.toggle('nav-collapsed', collapsed);
  if (!collapsed) closeMenu(false);
}

function openMenu(open) {
  menu.hidden = !open;
  toggle.setAttribute('aria-expanded', String(open));
}

// closeMenu closes the menu; focus inside it, or refocus, moves to the toggle.
function closeMenu(refocus) {
  if (menu.hidden) return;
  const inside = menu.contains(document.activeElement);
  openMenu(false);
  if ((refocus || inside) && collapsed) toggle.focus();
}

toggle.addEventListener('click', () => openMenu(menu.hidden));
current.addEventListener('click', () => openMenu(menu.hidden));
menu.addEventListener('click', (e) => {
  if (e.target.closest('a')) closeMenu(false);
});
nav.addEventListener('keydown', (e) => {
  if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
  if (menu.hidden) {
    if (e.target !== toggle || e.key !== 'ArrowDown') return;
    openMenu(true);
  }
  e.preventDefault();
  const i = items.indexOf(document.activeElement);
  const n = items.length;
  items[e.key === 'ArrowDown' ? (i + 1) % n : (i <= 0 ? n - 1 : i - 1)].focus();
});
nav.addEventListener('focusout', (e) => {
  if (e.relatedTarget && !nav.contains(e.relatedTarget)) closeMenu(false);
});
document.addEventListener('click', (e) => {
  if (!menu.hidden && !bar.contains(e.target) && !menu.contains(e.target)) closeMenu(false);
}, true);
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && !menu.hidden) {
    e.preventDefault();
    closeMenu(true);
  }
});

const resized = new ResizeObserver(fit);
resized.observe(topbar);
resized.observe(nav);
resized.observe(row);
fit();
if (document.fonts) {
  document.fonts.ready.then(fit);
  document.fonts.addEventListener('loadingdone', fit);
}

document.getElementById('logout').addEventListener('click', async () => {
  try {
    await logout();
  } catch {
    // the session ends either way
  }
  go('#/login');
});

// beforeRender marks the section on show in the top bar and closes its menu.
// The Telegram-style chat page (#/im) fills the window, with the top bar's
// links in its ≡ menu, so the top bar is hidden there and the body has the
// class chat-app, which its styles hang on; the classic chat page (#/chat)
// keeps the top bar.
function beforeRender(loc) {
  const section = loc.segments[0] || '';
  const im = section === 'im';
  topbar.hidden = section === 'login' || im;
  document.body.classList.toggle('chat-app', im);
  for (const a of [...links, ...items]) {
    const on = a.dataset.section === section;
    a.classList.toggle('active', on);
    if (on) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  }
  const shown = SECTIONS.find(([s]) => s === section);
  mount(current, shown ? shown[1] : '');
  closeMenu(false);
  userEl.textContent = currentUser();
  document.title = 'ConLoom 管理面板';
}

function notFound(ctx) {
  ctx.show(h('h1', null, '页面不存在'), h('p', null, h('a', { href: '#/' }, '返回概览')));
}

async function boot() {
  if (!(location.hash || '').startsWith('#/login')) {
    try {
      await loadSession();
    } catch {
      // a 401 has already moved to the login page; views report other errors
    }
  }
  start(main, notFound, beforeRender);
}

boot();
