/* Soundpad — клиентская часть.
 * Никаких зависимостей: обращается к JSON-API на том же адресе
 * и играет звук либо сам (когда ffmpeg недоступен), либо через ffmpeg
 * на сервере, который отдаёт готовый WAV. */

'use strict';

const PADS_PER_PAGE = 16;
const HOTKEYS = ['1', '2', '3', '4', 'q', 'w', 'e', 'r', 'a', 's', 'd', 'f', 'z', 'x', 'c', 'v'];

const state = {
  pages: [],
  volume: 100,
  padVol: 3,
  mode: 'none',
  dir: '',
  page: 0,
  moveId: null,       // звук, ожидающий переезда на другой пэд
  pendingSlot: 0,     // пэд, в который зальётся выбранный файл
};

const playing = new Map();   // id звука -> Set из HTMLAudioElement
const timers = new Map();    // id звука -> таймер снятия подсветки (режим ffplay)

const $ = (sel) => document.querySelector(sel);
const el = {
  grid: $('#grid'),
  pages: $('#pages'),
  vol: $('#vol'),
  volOut: $('#vol-out'),
  padvol: $('#padvol'),
  padvolOut: $('#padvol-out'),
  stop: $('#btn-stop'),
  status: $('#status'),
  subtitle: $('#subtitle'),
  modeBadge: $('#mode-badge'),
  libPath: $('#lib-path'),
  file: $('#file-input'),
  menu: $('#menu'),
  toasts: $('#toasts'),
  modal: $('#modal'),
  modalTitle: $('#modal-title'),
  modalLabel: $('#modal-label'),
  modalInput: $('#modal-input'),
  modalOk: $('#modal-ok'),
  modalCancel: $('#modal-cancel'),
};

/* ── API ─────────────────────────────────────────────────────────────── */

async function api(path, body) {
  const res = await fetch(path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* не JSON */ }
  if (!res.ok) throw new Error((data && data.error) || `HTTP ${res.status}`);
  return data;
}

async function errorText(res) {
  try {
    const data = await res.json();
    if (data && data.error) return data.error;
  } catch { /* ignore */ }
  return `HTTP ${res.status}`;
}

/* ── уведомления ─────────────────────────────────────────────────────── */

function toast(message, kind = 'info', ttl = 5000) {
  const node = document.createElement('div');
  node.className = `toast ${kind}`;
  node.textContent = message;
  el.toasts.append(node);
  setTimeout(() => {
    node.classList.add('hide');
    setTimeout(() => node.remove(), 300);
  }, ttl);
}

/* ── рендер ──────────────────────────────────────────────────────────── */

function currentPage() { return state.pages[state.page]; }
function currentTitle() {
  const page = currentPage();
  if (!page) return '';
  return page.title || `Страница ${state.page + 1}`;
}
function padLabel(n) { return String(n).padStart(2, '0'); }

function renderAll() {
  renderPages();
  renderGrid();
  renderStatus();
  $("#vol").value = state.volume;
  el.volOut.textContent = `${Math.round(state.volume)}%`;
  el.padvol.value = state.padVol;
  el.padvolOut.textContent = state.padVol === 0 ? 'до конца' : `${state.padVol.toFixed(1)} с`;
}

function renderPages() {
  el.pages.textContent = '';
  state.pages.forEach((page, index) => {
    const tab = document.createElement('button');
    tab.className = 'page-tab' + (index === state.page ? ' active' : '');
    tab.dataset.index = String(index);
    const title = document.createElement('span');
    title.textContent = page.title || `Страница ${index + 1}`;
    const count = page.sounds.filter(Boolean).length;
    const badge = document.createElement('em');
    badge.textContent = String(count);
    tab.append(title, badge);
    tab.addEventListener('click', () => switchPage(index));
    tab.addEventListener('contextmenu', (e) => {
      e.preventDefault();
      openPageMenu(e, index);
    });
    el.pages.append(tab);
  });

  const add = document.createElement('button');
  add.className = 'page-tab page-add';
  add.textContent = '+ страница';
  add.title = 'Добавить страницу';
  add.addEventListener('click', async () => {
    try {
      applyState(await api('/api/page', { action: 'add' }));
      switchPage(state.pages.length - 1);
    } catch (err) { toast(err.message, 'error'); }
  });
  el.pages.append(add);
}

function renderGrid() {
  el.grid.textContent = '';
  const page = currentPage();
  el.grid.classList.toggle('moving', Boolean(state.moveId));
  if (!page) return;

  for (let slot = 1; slot <= PADS_PER_PAGE; slot++) {
    const sound = page.sounds[slot - 1] || null;
    const pad = document.createElement('button');
    pad.className = 'pad' + (sound ? '' : ' empty');
    if (sound) pad.style.setProperty('--hue', String(sound.hue ?? 210));
    pad.dataset.slot = String(slot);
    if (sound && isPlaying(sound.id)) pad.classList.add('playing');
    if (state.moveId && sound && sound.id === state.moveId) pad.classList.add('moving-source');

    const key = document.createElement('span');
    key.className = 'pad-key';
    key.textContent = HOTKEYS[slot - 1].toUpperCase();

    const num = document.createElement('span');
    num.className = 'pad-num';
    num.textContent = padLabel(slot);

    if (sound) {
      const name = document.createElement('span');
      name.className = 'pad-name';
      name.textContent = sound.name;

      const meta = document.createElement('span');
      meta.className = 'pad-meta';
      const bits = [];
      if (sound.dur) bits.push(`${sound.dur.toFixed(2)} с`);
      bits.push(sound.built ? 'демо' : 'свой');
      if (sound.gain) bits.push(`×${sound.gain.toFixed(1)}`);
      meta.textContent = bits.join(' · ');

      const bars = document.createElement('span');
      bars.className = 'pad-bars';
      bars.append(document.createElement('i'), document.createElement('i'), document.createElement('i'));

      pad.append(num, name, meta, bars, key);
    } else {
      const plus = document.createElement('span');
      plus.className = 'pad-plus';
      plus.textContent = '+';
      const hint = document.createElement('span');
      hint.className = 'pad-hint';
      hint.textContent = 'перетащите файл';
      pad.append(num, plus, hint, key);
    }

    pad.addEventListener('click', (e) => {
      if (state.moveId) { dropMove(slot); return; }
      if (!sound) { pickFile(slot); return; }
      trigger(sound, { overlap: e.shiftKey });
    });
    pad.addEventListener('contextmenu', (e) => {
      e.preventDefault();
      if (state.moveId) { state.moveId = null; renderGrid(); return; }
      openPadMenu(e, slot, sound);
    });
    pad.addEventListener('dragstart', (e) => {
      if (!sound) { e.preventDefault(); return; }
      e.dataTransfer.setData('text/soundpad-id', sound.id);
      e.dataTransfer.setData('text/plain', sound.id);
      e.dataTransfer.effectAllowed = 'move';
    });
    pad.addEventListener('dragover', (e) => { e.preventDefault(); pad.classList.add('drop'); });
    pad.addEventListener('dragleave', () => pad.classList.remove('drop'));
    pad.addEventListener('drop', async (e) => {
      e.preventDefault();
      pad.classList.remove('drop');
      const id = e.dataTransfer.getData('text/soundpad-id');
      const files = e.dataTransfer.files;
      if (files && files.length) { await uploadFiles(slot, files); return; }
      if (id) await moveSound(id, page.name, slot);
    });

    el.grid.append(pad);
  }
}

function renderStatus() {
  const page = currentPage();
  const used = page ? page.sounds.filter(Boolean).length : 0;
  const labels = { ffmpeg: 'ffmpeg на сервере', ffplay: 'ffplay на сервере', none: 'воспроизведение браузером' };
  el.status.textContent = `${currentTitle()} · занято ${used}/${PADS_PER_PAGE} · ${labels[state.mode] || state.mode}`;
  el.modeBadge.textContent = labels[state.mode] || state.mode;
  el.modeBadge.classList.toggle('warn', state.mode === 'none');
  el.libPath.textContent = state.dir;
  el.subtitle.textContent = state.mode === 'none'
    ? 'ffmpeg не найден — играем WAV силами браузера'
    : 'клавиши 1–4 / qwert / asdf / zxcv, Shift — наложение, пробел — стоп';
}

/* ── воспроизведение ─────────────────────────────────────────────────── */

function isPlaying(id) {
  const set = playing.get(id);
  if (set && set.size > 0) return true;
  return timers.has(id);
}

function markPlaying(id, on, seconds) {
  if (on) {
    timers.set(id, setTimeout(() => { timers.delete(id); renderGrid(); }, Math.max(120, seconds * 1000)));
  } else {
    clearTimeout(timers.get(id));
    timers.delete(id);
  }
  renderGrid();
}

function soundById(id) {
  for (const page of state.pages) {
    for (const sound of page.sounds) if (sound && sound.id === id) return sound;
  }
  return null;
}

function padGain(sound) { return sound && sound.gain ? sound.gain : 1; }

function stopSound(id) {
  const set = playing.get(id);
  if (set) {
    for (const audio of set) { audio.pause(); audio.currentTime = 0; }
    playing.delete(id);
  }
  clearTimeout(timers.get(id));
  timers.delete(id);
  renderGrid();
}

function stopEverything() {
  for (const id of Array.from(playing.keys())) stopSound(id);
  for (const id of Array.from(timers.keys())) stopSound(id);
  api('/api/stop', {}).catch(() => {});
  renderGrid();
}

function trackLocal(id, audio) {
  if (!playing.has(id)) playing.set(id, new Set());
  const set = playing.get(id);
  set.add(audio);
  audio.addEventListener('ended', () => {
    set.delete(audio);
    if (set.size === 0) playing.delete(id);
    renderGrid();
  });
  audio.addEventListener('error', () => {
    set.delete(audio);
    toast('Браузер не смог проиграть файл. Установите ffmpeg для поддержки всех форматов.', 'error', 8000);
    renderGrid();
  });
}

/* Ядро: один запуск звука. */
async function trigger(sound, opts = {}) {
  const overlap = Boolean(opts.overlap);
  const wasPlaying = isPlaying(sound.id);
  if (!overlap && wasPlaying) { stopSound(sound.id); return; }
  if (!overlap) stopSound(sound.id);

  if (state.mode === 'none') {
    const audio = new Audio(`/api/audio?id=${encodeURIComponent(sound.id)}`);
    audio.volume = Math.min(1, (state.volume / 100) * padGain(sound));
    trackLocal(sound.id, audio);
    try { await audio.play(); } catch (err) { toast(`Не удалось запустить звук: ${err.message}`, 'error'); }
    return;
  }

  if (state.mode === 'ffplay') {
    try {
      const res = await api('/api/play', { id: sound.id, mode: overlap ? 'overlap' : 'toggle' });
      const dur = Math.min(sound.dur || 1, state.padVol > 0 ? state.padVol : (sound.dur || 1));
      markPlaying(sound.id, res.playing, dur);
    } catch (err) { toast(err.message, 'error'); }
    return;
  }

  // ffmpeg: сервер декодирует звук в WAV, играет браузер
  try {
    const res = await fetch('/api/play', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: sound.id, mode: overlap ? 'overlap' : 'start', gain: padGain(sound) }),
    });
    if (!res.ok) { toast(await errorText(res), 'error', 8000); return; }
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const audio = new Audio(url);
    trackLocal(sound.id, audio);
    audio.addEventListener('ended', () => URL.revokeObjectURL(url), { once: true });
    try { await audio.play(); }
    catch (err) { toast(`Не удалось запустить звук: ${err.message}`, 'error'); }
  } catch (err) {
    toast(`Ошибка воспроизведения: ${err.message}`, 'error');
  }
}

/* ── загрузка файлов ─────────────────────────────────────────────────── */

function pickFile(slot) {
  state.pendingSlot = slot;
  el.file.value = '';
  el.file.click();
}

el.file.addEventListener('change', async () => {
  const files = Array.from(el.file.files || []);
  if (files.length) await uploadFiles(state.pendingSlot || 1, files);
});

async function uploadFiles(startSlot, files) {
  const page = currentPage();
  if (!page) return;
  let slot = startSlot;
  for (const file of files) {
    if (slot > PADS_PER_PAGE) { toast('Пэды закончились — заведите новую страницу', 'warn'); break; }
    const form = new FormData();
    form.append('page', page.name);
    form.append('slot', String(slot));
    form.append('file', file, file.name);
    try {
      const res = await fetch('/api/upload', { method: 'POST', body: form });
      const text = await res.text();
      if (!res.ok) {
        let msg = `HTTP ${res.status}`;
        try { msg = JSON.parse(text).error || msg; } catch { /* ignore */ }
        toast(`${file.name}: ${msg}`, 'error', 8000);
      } else {
        applyState(JSON.parse(text));
        toast(`Загружено: ${file.name} → пэд ${slot}`, 'ok');
        slot += 1;
      }
    } catch (err) {
      toast(`${file.name}: ${err.message}`, 'error');
    }
  }
}

/* ── операции над пэдами и страницами ───────────────────────────────── */

async function moveSound(id, pageName, slot) {
  try {
    applyState(await api('/api/sound', { action: 'move', id, page: pageName, slot }));
    state.moveId = null;
    renderGrid();
  } catch (err) { toast(err.message, 'error'); }
}

function dropMove(slot) {
  const id = state.moveId;
  const page = currentPage();
  state.moveId = null;
  if (id) moveSound(id, page.name, slot);
}

async function renameSound(sound) {
  const name = await askText('Как назвать звук?', sound.name);
  if (name === null) return;
  try { applyState(await api('/api/sound', { action: 'rename', id: sound.id, name })); }
  catch (err) { toast(err.message, 'error'); }
}

async function deleteSound(sound) {
  if (!confirm(`Удалить «${sound.name}»?`)) return;
  try { applyState(await api('/api/sound', { action: 'delete', id: sound.id })); }
  catch (err) { toast(err.message, 'error'); }
}

async function setGain(sound, gain) {
  try { applyState(await api('/api/sound', { action: 'gain', id: sound.id, gain })); }
  catch (err) { toast(err.message, 'error'); }
}

async function renamePage(index) {
  const page = state.pages[index];
  const title = await askText('Название страницы', page.title || `Страница ${index + 1}`);
  if (title === null) return;
  try { applyState(await api('/api/page', { action: 'title', name: page.name, title })); }
  catch (err) { toast(err.message, 'error'); }
}

async function deletePage(index) {
  const page = state.pages[index];
  if (!confirm(`Удалить страницу «${currentTitle()}» вместе со звуками?`)) return;
  try {
    applyState(await api('/api/page', { action: 'delete', name: page.name }));
    state.page = Math.max(0, Math.min(state.page, state.pages.length - 1));
    renderAll();
  } catch (err) { toast(err.message, 'error'); }
}

function switchPage(index) {
  if (index < 0 || index >= state.pages.length) return;
  state.page = index;
  state.moveId = null;
  renderAll();
}

/* ── контекстное меню ────────────────────────────────────────────────── */

function closeMenu() { el.menu.hidden = true; el.menu.textContent = ''; }

function openMenu(x, y, items) {
  el.menu.textContent = '';
  for (const item of items) {
    if (item.separator) {
      const hr = document.createElement('hr');
      el.menu.append(hr);
      continue;
    }
    if (item.slider) {
      const row = document.createElement('label');
      row.className = 'menu-slider';
      const caption = document.createElement('span');
      caption.textContent = `Громкость пэда: ×${item.value.toFixed(1)}`;
      const input = document.createElement('input');
      input.type = 'range';
      input.min = '0.1';
      input.max = '3';
      input.step = '0.1';
      input.value = String(item.value);
      input.addEventListener('input', () => { caption.textContent = `Громкость пэда: ×${Number(input.value).toFixed(1)}`; });
      input.addEventListener('change', () => item.onChange(Number(input.value)));
      row.append(caption, input);
      el.menu.append(row);
      continue;
    }
    const btn = document.createElement('button');
    btn.textContent = item.label;
    if (item.danger) btn.classList.add('danger');
    if (item.disabled) btn.disabled = true;
    btn.addEventListener('click', () => { closeMenu(); item.onClick && item.onClick(); });
    el.menu.append(btn);
  }
  el.menu.hidden = false;
  const rect = el.menu.getBoundingClientRect();
  const maxX = window.innerWidth - rect.width - 8;
  const maxY = window.innerHeight - rect.height - 8;
  el.menu.style.left = `${Math.max(8, Math.min(x, maxX))}px`;
  el.menu.style.top = `${Math.max(8, Math.min(y, maxY))}px`;
}

function openPadMenu(event, slot, sound) {
  const items = [];
  if (sound) {
    items.push({ label: '▶ Играть (с наложением)', onClick: () => trigger(sound, { overlap: true }) });
    items.push({ label: '⏹ Остановить', onClick: () => stopSound(sound.id) });
    items.push({ separator: true });
    items.push({ label: '✏️ Переименовать', onClick: () => renameSound(sound) });
    items.push({ slider: true, value: padGain(sound), onChange: (v) => setGain(sound, v) });
    items.push({ label: '🔀 Переместить…', onClick: () => { state.moveId = sound.id; renderGrid(); toast('Выберите пэд назначения (Esc — отмена)'); } });
    items.push({ separator: true });
    items.push({ label: '⬆️ Заменить файл…', onClick: () => pickFile(slot) });
    items.push({ label: '🗑 Удалить', danger: true, onClick: () => deleteSound(sound) });
  } else {
    items.push({ label: '⬆️ Загрузить звук…', onClick: () => pickFile(slot) });
  }
  openMenu(event.clientX, event.clientY, items);
}

function openPageMenu(event, index) {
  openMenu(event.clientX, event.clientY, [
    { label: '✏️ Переименовать страницу', onClick: () => renamePage(index) },
    { label: '🗑 Удалить страницу', danger: true, disabled: state.pages.length <= 1, onClick: () => deletePage(index) },
  ]);
}

/* ── модальное окно ввода ────────────────────────────────────────────── */

let modalResolve = null;
function askText(title, value) {
  el.modalTitle.textContent = title;
  el.modalInput.value = value || '';
  el.modal.hidden = false;
  el.modalInput.focus();
  el.modalInput.select();
  return new Promise((resolve) => { modalResolve = resolve; });
}
function closeModal(result) {
  el.modal.hidden = true;
  if (modalResolve) { modalResolve(result); modalResolve = null; }
}
el.modalOk.addEventListener('click', () => closeModal(el.modalInput.value.trim()));
el.modalCancel.addEventListener('click', () => closeModal(null));
el.modalInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter') closeModal(el.modalInput.value.trim());
  if (e.key === 'Escape') closeModal(null);
});
el.modal.addEventListener('click', (e) => { if (e.target === el.modal) closeModal(null); });

/* ── горячие клавиши ─────────────────────────────────────────────────── */

document.addEventListener('keydown', (e) => {
  if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') return;
  if (!el.modal.hidden) return;

  if (e.code === 'Space') { e.preventDefault(); stopEverything(); return; }
  if (e.key === 'Escape') { closeMenu(); state.moveId = null; renderGrid(); return; }
  if (e.key === 'PageDown' || e.key === ']') { e.preventDefault(); switchPage(state.page + 1); return; }
  if (e.key === 'PageUp' || e.key === '[') { e.preventDefault(); switchPage(state.page - 1); return; }

  const index = HOTKEYS.indexOf(e.key.toLowerCase());
  if (index === -1) return;
  const page = currentPage();
  const sound = page && page.sounds[index];
  if (!sound) return;
  e.preventDefault();
  trigger(sound, { overlap: e.shiftKey });
});

document.addEventListener('click', (e) => { if (!el.menu.contains(e.target)) closeMenu(); });
window.addEventListener('blur', closeMenu);

/* ── громкость и настройки ───────────────────────────────────────────── */

let volTimer = null;
el.vol.addEventListener('input', () => {
  state.volume = Number(el.vol.value);
  el.volOut.textContent = `${Math.round(state.volume)}%`;
  for (const set of playing.values()) {
    for (const audio of set) audio.volume = Math.min(1, state.volume / 100);
  }
  clearTimeout(volTimer);
  volTimer = setTimeout(() => {
    api('/api/volume', { value: state.volume }).catch((err) => toast(err.message, 'error'));
  }, 250);
});

let padVolTimer = null;
el.padvol.addEventListener('input', () => {
  state.padVol = Number(el.padvol.value);
  el.padvolOut.textContent = state.padVol === 0 ? 'до конца' : `${state.padVol.toFixed(1)} с`;
  clearTimeout(padVolTimer);
  padVolTimer = setTimeout(() => {
    api('/api/padvol', { value: state.padVol }).catch((err) => toast(err.message, 'error'));
  }, 250);
});

el.stop.addEventListener('click', stopEverything);

/* ── состояние с сервера ─────────────────────────────────────────────── */

function applyState(payload) {
  if (!payload) return;
  state.pages = payload.pages || [];
  state.volume = payload.volume ?? 100;
  state.padVol = payload.padVol ?? 3;
  state.mode = payload.mode || 'none';
  state.dir = payload.dir || '';
  if (state.page >= state.pages.length) state.page = Math.max(0, state.pages.length - 1);
  renderAll();
}

async function boot() {
  try {
    applyState(await api('/api/state'));
  } catch (err) {
    toast(`Не удалось получить состояние: ${err.message}`, 'error', 10000);
    return;
  }
  // Заодно проверяем, умеет ли браузер играть WAV — предупреждаем заранее.
  if (state.mode === 'none') {
    toast('ffmpeg не найден: играем WAV-файлы напрямую. Для mp3/ogg/flac установите FFmpeg.', 'warn', 12000);
  }
}

/* Перетаскивание файлов на всю сетку: заливаем в первый пэд. */
document.addEventListener('dragover', (e) => {
  if (e.dataTransfer && Array.from(e.dataTransfer.types).includes('Files')) e.preventDefault();
});
document.addEventListener('drop', (e) => {
  if (!e.dataTransfer || !e.dataTransfer.files.length) return;
  e.preventDefault();
  const page = currentPage();
  if (!page) return;
  const firstFree = page.sounds.findIndex((s) => !s) + 1;
  uploadFiles(firstFree || 1, e.dataTransfer.files);
});

boot();
