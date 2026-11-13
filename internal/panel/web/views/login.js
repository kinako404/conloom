import { h, mount, field, errorBox } from '../lib/dom.js';
import { login } from '../lib/api.js';
import { go } from '../lib/router.js';

// afterLogin is the page to return to: next when it is a panel route.
function afterLogin(next) {
  return next && next.startsWith('#/') && !next.startsWith('#/login') ? next : '#/';
}

export function loginView(ctx) {
  const name = h('input', { name: 'name', autocomplete: 'username', required: true });
  const password = h('input', { type: 'password', name: 'password', autocomplete: 'current-password', required: true });
  const msg = h('div');
  const submit = h('button', { type: 'submit', class: 'primary' }, '登录');
  const form = h('form', {
    class: 'card login',
    onsubmit: async (e) => {
      e.preventDefault();
      submit.disabled = true;
      mount(msg);
      try {
        await login(name.value, password.value);
        go(afterLogin(ctx.query.get('next')));
      } catch (err) {
        password.value = '';
        let text = err.message;
        if (err.status === 429 && err.retryAfter) text += `（请 ${err.retryAfter} 秒后再试）`;
        mount(msg, errorBox({ message: text }));
      } finally {
        submit.disabled = false;
      }
    },
  },
  h('h1', null, 'ConLoom 管理面板'),
  field('用户名', name),
  field('密码', password),
  msg,
  submit);
  ctx.show(form);
  name.focus();
}
