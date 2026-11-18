// The crop dialog of a persona template's avatar: a square chosen on an
// image, drawn at AVATAR_SIZE pixels and uploaded as a persona image.

import { h, mount, errorBox } from '../lib/dom.js';
import { upload, isAbort } from '../lib/api.js';

// AVATAR_SIZE is the width and height of a cropped avatar, in pixels.
export const AVATAR_SIZE = 512;
// MIN_CROP is the smallest selection, in pixels of the image.
const MIN_CROP = 64;
// KEY_STEP is how far an arrow key moves the selection, and + or - grows or
// shrinks it, in pixels of the screen.
const KEY_STEP = 8;
// PREVIEW_SIZE is the size of the round preview, in CSS pixels.
const PREVIEW_SIZE = 96;
// CORNERS are the resize handles: the direction each grows the selection in.
const CORNERS = { nw: [-1, -1], ne: [1, -1], sw: [-1, 1], se: [1, 1] };

const clamp = (v, lo, hi) => Math.min(Math.max(v, lo), hi);

// loadImage resolves to the decoded image at src, a data: URL or an address
// of this panel, so that a canvas it is drawn on can still be read.
function loadImage(src) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.addEventListener('load', () => {
      if (img.naturalWidth && img.naturalHeight) resolve(img);
      else reject(new Error('无法读取这张图片'));
    }, { once: true });
    img.addEventListener('error', () => reject(new Error('无法读取这张图片，请换一张 PNG、JPEG、WebP 或 GIF 图片')), { once: true });
    img.src = src;
  });
}

function canvasOf(width, height) {
  return h('canvas', { width, height });
}

function smooth(ctx) {
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = 'high';
  return ctx;
}

// drawSquare draws the square (x, y, size) of src onto a new canvas of
// out × out pixels, halving a large square step by step first, which keeps a
// strong reduction smooth.
function drawSquare(src, x, y, size, out) {
  let from = src;
  let fx = x;
  let fy = y;
  let fs = size;
  while (fs > out * 2) {
    const half = Math.ceil(fs / 2);
    const step = canvasOf(half, half);
    smooth(step.getContext('2d')).drawImage(from, fx, fy, fs, fs, 0, 0, half, half);
    from = step;
    fx = 0;
    fy = 0;
    fs = half;
  }
  const canvas = canvasOf(out, out);
  smooth(canvas.getContext('2d')).drawImage(from, fx, fy, fs, fs, 0, 0, out, out);
  return canvas;
}

function toBlob(canvas, type, quality) {
  return new Promise((resolve, reject) => {
    canvas.toBlob((blob) => (blob ? resolve(blob) : reject(new Error('无法生成头像图片'))), type, quality);
  });
}

// avatarBlob is the selection of img as an AVATAR_SIZE square PNG or, when
// that is over maxBytes, a JPEG of quality 0.9 on white.
async function avatarBlob(img, sel, maxBytes) {
  const canvas = drawSquare(img, Math.round(sel.x), Math.round(sel.y), Math.round(sel.size), AVATAR_SIZE);
  const png = await toBlob(canvas, 'image/png');
  if (png.size <= maxBytes) return png;
  const flat = canvasOf(AVATAR_SIZE, AVATAR_SIZE);
  const ctx = flat.getContext('2d');
  ctx.fillStyle = '#ffffff';
  ctx.fillRect(0, 0, AVATAR_SIZE, AVATAR_SIZE);
  ctx.drawImage(canvas, 0, 0);
  const jpeg = await toBlob(flat, 'image/jpeg', 0.9);
  if (jpeg.size > maxBytes) throw new Error('头像图片太大，无法上传');
  return jpeg;
}

// cropAvatar opens a dialog in parent over the image at src (a data: URL or
// an address of this panel) to choose a square of it: at first the largest
// one in the middle, moved by dragging it and resized, staying square and
// inside the image and at least MIN_CROP image pixels, by dragging a corner,
// with a mouse, a pen or a finger, or with the arrow keys and + / -. A round
// preview follows the selection. 确定 draws it at AVATAR_SIZE pixels and
// uploads it as a persona image of at most maxBytes; the promise resolves to
// the upload's answer ({id, …}) then, or to null on 取消. It rejects, without
// a dialog, when the image cannot be loaded. The dialog's listeners go and
// the dialog is removed when it closes.
export async function cropAvatar({ src, parent, maxBytes }) {
  const img = await loadImage(src);
  const W = img.naturalWidth;
  const H = img.naturalHeight;
  const minSize = Math.min(MIN_CROP, W, H);
  const maxSize = Math.min(W, H);
  const sel = { x: (W - maxSize) / 2, y: (H - maxSize) / 2, size: maxSize };
  let scale = 1;
  let drag = null;
  let busy = false;

  img.className = 'crop-image';
  img.alt = '';
  img.draggable = false;
  const handles = Object.keys(CORNERS).map((corner) => h('span', { class: 'crop-handle', dataset: { corner } }));
  const box = h('div', { class: 'crop-box', tabindex: 0, role: 'group', 'aria-label': '头像选区：方向键移动，+ / - 缩放' }, handles);
  const frame = h('div', { class: 'crop-frame' }, img, box);
  const stage = h('div', { class: 'crop-stage' }, frame);
  const preview = h('canvas', { class: 'crop-preview', width: PREVIEW_SIZE * 2, height: PREVIEW_SIZE * 2 });
  const previewCtx = smooth(preview.getContext('2d'));
  const msg = h('div');
  const ok = h('button', { type: 'button', class: 'primary' }, '确定');
  const cancel = h('button', { type: 'button' }, '取消');
  const dialog = h('dialog', { class: 'card dialog wide crop-dialog' },
    h('h2', null, '裁剪头像'),
    h('p', { class: 'hint' }, '拖动方框移动，拖动四角调整大小；也可以先点一下方框，再用方向键移动、+ / - 缩放。'),
    stage,
    h('div', { class: 'crop-side' }, preview, h('span', { class: 'hint' }, '预览')),
    msg,
    h('div', { class: 'toolbar' }, ok, cancel));

  const offs = [];
  const listen = (el, type, fn, opts) => {
    el.addEventListener(type, fn, opts);
    offs.push(() => el.removeEventListener(type, fn, opts));
  };

  const set = (x, y, size) => {
    sel.size = clamp(size, minSize, maxSize);
    sel.x = clamp(x, 0, W - sel.size);
    sel.y = clamp(y, 0, H - sel.size);
    draw();
  };
  const draw = () => {
    box.style.left = `${sel.x * scale}px`;
    box.style.top = `${sel.y * scale}px`;
    box.style.width = `${sel.size * scale}px`;
    box.style.height = `${sel.size * scale}px`;
    previewCtx.clearRect(0, 0, preview.width, preview.height);
    previewCtx.drawImage(img, sel.x, sel.y, sel.size, sel.size, 0, 0, preview.width, preview.height);
  };
  const layout = () => {
    const width = stage.clientWidth || 480;
    const height = Math.max(160, Math.min(window.innerHeight * 0.55, 480));
    scale = Math.min(width / W, height / H);
    frame.style.width = `${W * scale}px`;
    frame.style.height = `${H * scale}px`;
    draw();
  };
  // at is the point of the image under a pointer event.
  const at = (e) => {
    const r = frame.getBoundingClientRect();
    return { x: (e.clientX - r.left) / scale, y: (e.clientY - r.top) / scale };
  };

  listen(frame, 'pointerdown', (e) => {
    if (drag || busy || !e.isPrimary || e.button !== 0) return;
    const corner = e.target.dataset ? e.target.dataset.corner : undefined;
    if (!corner && e.target !== box) return;
    e.preventDefault();
    box.focus();
    frame.setPointerCapture(e.pointerId);
    const p = at(e);
    if (corner) {
      const [cx, cy] = CORNERS[corner];
      drag = { id: e.pointerId, corner, ax: cx > 0 ? sel.x : sel.x + sel.size, ay: cy > 0 ? sel.y : sel.y + sel.size };
    } else {
      drag = { id: e.pointerId, dx: p.x - sel.x, dy: p.y - sel.y };
    }
  });
  listen(frame, 'pointermove', (e) => {
    if (!drag || e.pointerId !== drag.id) return;
    e.preventDefault();
    const p = at(e);
    if (!drag.corner) {
      set(p.x - drag.dx, p.y - drag.dy, sel.size);
      return;
    }
    const [cx, cy] = CORNERS[drag.corner];
    const { ax, ay } = drag;
    const room = Math.min(cx > 0 ? W - ax : ax, cy > 0 ? H - ay : ay);
    const size = clamp(Math.max(cx * (p.x - ax), cy * (p.y - ay)), minSize, Math.max(minSize, room));
    set(cx > 0 ? ax : ax - size, cy > 0 ? ay : ay - size, size);
  });
  const release = (e) => {
    if (drag && e.pointerId === drag.id) drag = null;
  };
  listen(frame, 'pointerup', release);
  listen(frame, 'pointercancel', release);
  listen(frame, 'lostpointercapture', release);

  listen(box, 'keydown', (e) => {
    if (busy) return;
    const step = KEY_STEP / scale;
    const moves = { ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] };
    let grow = 0;
    if (e.key === '+' || e.key === '=') grow = step;
    else if (e.key === '-' || e.key === '_') grow = -step;
    if (moves[e.key]) {
      const [dx, dy] = moves[e.key];
      set(sel.x + dx, sel.y + dy, sel.size);
    } else if (grow) {
      const size = clamp(sel.size + grow, minSize, maxSize);
      set(sel.x + (sel.size - size) / 2, sel.y + (sel.size - size) / 2, size);
    } else {
      return;
    }
    e.preventDefault();
  });
  listen(window, 'resize', layout);
  listen(dialog, 'cancel', (e) => {
    if (busy) e.preventDefault();
  });

  return new Promise((resolve) => {
    let result = null;
    listen(cancel, 'click', () => dialog.close());
    listen(ok, 'click', async () => {
      busy = true;
      ok.disabled = true;
      cancel.disabled = true;
      mount(msg, h('p', { class: 'muted' }, '正在上传…'));
      try {
        const blob = await avatarBlob(img, sel, maxBytes);
        result = await upload('persona-images', blob);
        busy = false;
        dialog.close();
      } catch (err) {
        if (!isAbort(err)) mount(msg, errorBox(err));
      } finally {
        busy = false;
        ok.disabled = false;
        cancel.disabled = false;
      }
    });
    dialog.addEventListener('close', () => {
      for (const off of offs) off();
      drag = null;
      dialog.remove();
      resolve(result);
    }, { once: true });
    parent.appendChild(dialog);
    dialog.showModal();
    layout();
    box.focus();
  });
}
