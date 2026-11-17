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
route('/config', configView);

const topbar = document.getElementById('topbar');
const nav = document.getElementById('nav');
const userEl = document.getElementById('user');
const main = document.getElementById('main');

const links = SECTIONS.map(([section, title]) => {
  const a = h('a', { href: '#/' + section }, title);
  a.dataset.section = section;
  return a;
});
mount(nav, links);

document.getElementById('logout').addEventListener('click', async () => {
  try {
    await logout();
  } catch {
    // the session ends either way
  }
  go('#/login');
});

// beforeRender marks the section on show in the top bar. The Telegram-style
// chat page (#/im) fills the window, with the top bar's links in its ≡ menu,
// so the top bar is hidden there and the body has the class chat-app, which
// its styles hang on; the classic chat page (#/chat) keeps the top bar.
function beforeRender(loc) {
  const section = loc.segments[0] || '';
  const im = section === 'im';
  topbar.hidden = section === 'login' || im;
  document.body.classList.toggle('chat-app', im);
  for (const a of links) a.classList.toggle('active', a.dataset.section === section);
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
