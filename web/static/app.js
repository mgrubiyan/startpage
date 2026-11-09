'use strict';

// Search prefixes. Navigation happens in the browser; the server only supplies suggestions.
const ENGINES = {
  g:  { name: 'Google',     url: 'https://www.google.com/search?q=' },
  y:  { name: 'Яндекс',     url: 'https://yandex.ru/search/?text=' },
  d:  { name: 'DuckDuckGo', url: 'https://duckduckgo.com/?q=' },
  gh: { name: 'GitHub',     url: 'https://github.com/search?type=repositories&q=' },
  yt: { name: 'YouTube',    url: 'https://www.youtube.com/results?search_query=' },
  w:  { name: 'Википедия',  url: 'https://ru.wikipedia.org/w/index.php?search=' },
};

const $ = (id) => document.getElementById(id);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

const state = { settings: { searchEngine: 'g', city: '' } };

// ---------- API ----------
class AuthError extends Error {}

async function api(path, { method = 'GET', body, signal, keepalive = false } = {}) {
  // keepalive lets a save started as the tab goes away outlive the page.
  const init = { method, signal, keepalive, credentials: 'same-origin', headers: {} };
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  if (res.status === 401 && path !== '/api/login') {
    showLogin();
    throw new AuthError('unauthorized');
  }
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const data = await res.json();
      if (data && data.error) msg = data.error;
    } catch { /* body was not JSON */ }
    const err = new Error(msg);
    err.status = res.status;
    throw err;
  }
  return res.status === 204 ? null : res.json();
}

const ignorable = (e) => e instanceof AuthError || e.name === 'AbortError';

// ---------- URLs ----------
function safeUrl(raw) {
  try {
    const u = new URL(raw);
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : null;
  } catch { return null; }
}

function looksLikeAddress(s) {
  if (/\s/.test(s)) return null;
  if (/^https?:\/\//i.test(s)) return safeUrl(s);
  if (/^(localhost|\d{1,3}(\.\d{1,3}){3})(:\d+)?(\/\S*)?$/i.test(s)) return safeUrl('http://' + s);
  if (/^[\w-]+(\.[\w-]+)*\.[a-zа-я]{2,}(:\d+)?([/?#]\S*)?$/i.test(s)) return safeUrl('https://' + s);
  return null;
}

function parsePrefix(s) {
  const m = s.match(/^(\S+)\s+(.*)$/);
  if (m && ENGINES[m[1].toLowerCase()]) return { prefix: m[1].toLowerCase(), text: m[2].trim() };
  return { prefix: '', text: s };
}

function resolveQuery(raw) {
  const s = raw.trim();
  if (!s) return null;
  const { prefix, text } = parsePrefix(s);
  if (prefix && text) {
    const e = ENGINES[prefix];
    return { url: e.url + encodeURIComponent(text), label: e.name };
  }
  const addr = looksLikeAddress(s);
  if (addr) return { url: addr, label: 'Открыть' };
  const e = ENGINES[state.settings.searchEngine] || ENGINES.g;
  return { url: e.url + encodeURIComponent(s), label: e.name };
}

// ---------- timers ----------
let timers = [];
const every = (fn, ms) => timers.push(setInterval(fn, ms));
const stopTimers = () => { timers.forEach(clearInterval); timers = []; };

// ---------- login ----------
function showLogin() {
  stopTimers();
  clearArt(); // the painting comes from a private route; the login screen stays plain
  $('app').hidden = true;
  $('login').hidden = false;
  $('login-password').focus();
}

function showApp() {
  $('login').hidden = true;
  $('app').hidden = false;
  $('q').focus();
}

$('login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const err = $('login-error');
  err.textContent = '';
  try {
    await api('/api/login', { method: 'POST', body: { password: $('login-password').value } });
    $('login-form').reset();
    boot();
  } catch (ex) {
    err.textContent = ex.status === 401 ? 'Неверный пароль'
      : ex.status === 429 ? 'Слишком много попыток, подожди минуту'
      : 'Сервер недоступен: ' + ex.message;
  }
});

$('logout').addEventListener('click', async () => {
  try { await api('/api/logout', { method: 'POST', body: {} }); } catch { /* cookie is cleared either way */ }
  showLogin();
});

// ---------- clock ----------
function tick() {
  const now = new Date();
  $('time').textContent = now.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
  const d = now.toLocaleDateString('ru-RU', { weekday: 'long', day: 'numeric', month: 'long' });
  $('date').textContent = d.charAt(0).toUpperCase() + d.slice(1);
}

// ---------- weather ----------
const fmtTemp = (t) => { const r = Math.round(t); return (r > 0 ? '+' : '') + r + '°'; };

async function loadWeather() {
  try {
    const w = await api('/api/weather');
    $('weather-city').textContent = w.location;
    $('weather-value').textContent = `${w.icon} ${fmtTemp(w.temperature)} · ${Math.round(w.windMs)} м/с`;
    $('weather').title = `${w.description}, ощущается как ${fmtTemp(w.feelsLike)}` + (w.stale ? ' · данные устарели' : '');
    renderForecast(w.days || [], w.stale);
  } catch (e) {
    if (ignorable(e)) return;
    $('weather-city').textContent = state.settings.city || 'Погода';
    $('weather-value').textContent = '—';
    $('weather').title = 'Погода недоступна: ' + e.message;
    renderForecast([], false, 'Прогноз недоступен');
  }
}

// Days arrive as "YYYY-MM-DD" in the city's calendar. new Date("2026-11-10")
// would be UTC midnight, i.e. the 9th west of Greenwich, so build it from parts.
function dayLabel(iso, i) {
  if (i === 0) return 'Сегодня'; // the server already dropped days over in the city
  if (i === 1) return 'Завтра';
  const [y, m, d] = iso.split('-').map(Number);
  const s = new Date(y, m - 1, d).toLocaleDateString('ru-RU', { weekday: 'short', day: 'numeric' });
  return s.charAt(0).toUpperCase() + s.slice(1);
}

const fmtDaylight = (min) => `${Math.floor(min / 60)} ч ${min % 60} мин`;

function renderForecast(days, stale, emptyText = 'Нет данных') {
  const ol = $('forecast');
  ol.textContent = '';
  ol.classList.toggle('stale', stale);
  const today = days[0];
  const sun = $('sun');
  sun.textContent = today && today.sunrise
    ? `↑ ${today.sunrise}  ↓ ${today.sunset} · ${fmtDaylight(today.daylightMinutes)}`
    : '';
  sun.title = sun.textContent ? 'Восход, закат и долгота дня' : '';
  if (!days.length) { ol.append(el('li', 'empty', emptyText)); return; }
  days.forEach((d, i) => {
    const li = el('li');
    // Below 10% it's noise; the empty slot keeps the cards the same height.
    const p = d.precipitation;
    li.append(
      el('span', 'day', dayLabel(d.date, i)),
      el('span', 'icon-lg', d.icon),
      el('span', 'temps', `${fmtTemp(d.max)} / ${fmtTemp(d.min)}`),
      el('span', 'precip', p != null && p >= 10 ? `💧 ${p}%` : ''),
    );
    const sunLine = d.sunrise ? `\nвосход ${d.sunrise}, закат ${d.sunset}` : '';
    li.title = (d.description || '') + (p != null ? `, осадки ${p}%` : '') + sunLine + (stale ? '\nданные устарели' : '');
    ol.append(li);
  });
}

// ---------- rates ----------
async function loadRates() {
  const box = $('rates-value');
  try {
    const r = await api('/api/rates');
    box.textContent = '';
    [['$', r.usd], ['€', r.eur]].forEach(([sym, v], i) => {
      if (i) box.append(' ');
      box.append(`${sym} ${v.value.toFixed(2)}`);
      const diff = v.value - v.previous;
      if (Math.abs(diff) >= 0.005) box.append(el('span', diff > 0 ? 'up' : 'down', diff > 0 ? '▲' : '▼'));
    });
    $('rates').title = 'Курс ЦБ РФ на ' + new Date(r.date).toLocaleDateString('ru-RU') + (r.stale ? ' · данные устарели' : '');
  } catch (e) {
    if (ignorable(e)) return;
    box.textContent = '—';
    $('rates').title = 'Курсы недоступны: ' + e.message;
  }
}

// ---------- bookmarks ----------
function hue(str) {
  let h = 0;
  for (const ch of str) h = (h * 31 + ch.codePointAt(0)) % 360;
  return h;
}

function bookmarkTile(bm) {
  const url = safeUrl(bm.url);
  if (!url) return null;
  const host = new URL(url).hostname;
  const li = el('li');
  const a = el('a');
  a.href = url;
  const icon = el('span', 'icon', (bm.title || host).trim().charAt(0).toUpperCase());
  icon.style.background = `hsl(${hue(host)} 55% 50%)`;
  const img = el('img');
  img.alt = '';
  img.src = 'https://www.google.com/s2/favicons?sz=64&domain=' + encodeURIComponent(host);
  img.addEventListener('load', () => { icon.textContent = ''; icon.style.background = 'transparent'; icon.append(img); });
  a.append(icon, el('span', 'label', bm.title || host));
  const rm = el('button', 'remove', '×');
  rm.type = 'button';
  rm.title = 'Удалить';
  rm.addEventListener('click', async () => {
    try {
      await api(`/api/bookmarks/${bm.id}`, { method: 'DELETE' });
      loadBookmarks();
    } catch (e) {
      if (!ignorable(e)) $('bm-error').textContent = e.message;
    }
  });
  li.append(a, rm);
  return li;
}

async function loadBookmarks() {
  try {
    const items = await api('/api/bookmarks');
    const ul = $('bookmarks');
    ul.textContent = '';
    items.forEach((bm) => {
      const tile = bookmarkTile(bm);
      if (tile) ul.append(tile);
    });
  } catch (e) {
    if (!ignorable(e)) $('bm-error').textContent = 'Закладки не загрузились: ' + e.message;
  }
}

function toggleEditing(listId, formId, button, firstInputId) {
  const ul = $(listId);
  const on = !ul.classList.contains('editing');
  ul.classList.toggle('editing', on);
  $(formId).hidden = !on;
  button.setAttribute('aria-pressed', String(on));
  button.textContent = on ? 'Готово' : 'Изменить';
  if (on) $(firstInputId).focus();
}

$('edit-toggle').addEventListener('click', (e) => toggleEditing('bookmarks', 'add-bookmark', e.currentTarget, 'bm-title'));

$('add-bookmark').addEventListener('submit', async (e) => {
  e.preventDefault();
  const form = e.currentTarget;
  const err = $('bm-error');
  const raw = $('bm-url').value.trim();
  const url = safeUrl(raw) || looksLikeAddress(raw);
  if (!url) { err.textContent = 'Нужен адрес вида https://example.com'; return; }
  try {
    await api('/api/bookmarks', { method: 'POST', body: { title: $('bm-title').value, url } });
    err.textContent = '';
    form.reset();
    $('bm-title').focus();
    loadBookmarks();
  } catch (ex) {
    if (!ignorable(ex)) err.textContent = ex.message;
  }
});

// ---------- services ----------
function serviceMeta(s) {
  const c = s.lastCheck;
  if (!c) return 'ещё не проверялся';
  const latency = c.latencyMs < 1 ? '<1 мс' : `${c.latencyMs} мс`;
  const head = c.ok ? latency : (c.statusCode ? `HTTP ${c.statusCode}` : 'недоступен');
  if (s.uptime == null) return head;
  return `${head} · ${s.uptime.toFixed(Number.isInteger(s.uptime) ? 0 : 1)}% за сутки`;
}

async function loadServices() {
  try {
    const items = await api('/api/services');
    const ul = $('services');
    ul.textContent = '';
    if (!items.length) {
      ul.append(el('li', 'empty', 'Пока пусто. Нажми «Изменить» и добавь адрес /health бота.'));
      return;
    }
    items.forEach((s) => {
      const li = el('li');
      const status = s.lastCheck ? (s.lastCheck.ok ? 'ok' : 'fail') : '';
      li.append(el('span', 'dot ' + status), el('span', 'name', s.name), el('span', 'meta', serviceMeta(s)));
      if (s.lastCheck) {
        const at = new Date(s.lastCheck.checkedAt).toLocaleTimeString('ru-RU');
        li.title = `${s.url}\nпроверен в ${at}` + (s.lastCheck.error ? `\n${s.lastCheck.error}` : '');
      } else {
        li.title = s.url;
      }
      const rm = el('button', 'remove-inline', '×');
      rm.type = 'button';
      rm.title = 'Удалить';
      rm.addEventListener('click', async () => {
        try {
          await api(`/api/services/${s.id}`, { method: 'DELETE' });
          loadServices();
        } catch (e) {
          if (!ignorable(e)) $('svc-error').textContent = e.message;
        }
      });
      li.append(rm);
      ul.append(li);
    });
  } catch (e) {
    if (!ignorable(e)) $('services').textContent = 'Статусы не загрузились: ' + e.message;
  }
}

$('services-edit').addEventListener('click', (e) => toggleEditing('services', 'add-service', e.currentTarget, 'svc-name'));

$('add-service').addEventListener('submit', async (e) => {
  e.preventDefault();
  const form = e.currentTarget;
  const err = $('svc-error');
  const raw = $('svc-url').value.trim();
  const url = safeUrl(raw) || looksLikeAddress(raw);
  if (!url) { err.textContent = 'Нужен адрес вида https://example.com/health'; return; }
  try {
    await api('/api/services', { method: 'POST', body: { name: $('svc-name').value, url } });
    err.textContent = '';
    form.reset();
    loadServices();
    // The server probes a new service right away; pick the result up shortly.
    setTimeout(loadServices, 2000);
  } catch (ex) {
    if (!ignorable(ex)) err.textContent = ex.message;
  }
});

// ---------- settings ----------
Object.entries(ENGINES).forEach(([key, e]) => {
  const o = el('option', null, `${e.name} (${key})`);
  o.value = key;
  $('set-engine').append(o);
});

$('settings-toggle').addEventListener('click', (e) => {
  const form = $('settings');
  form.hidden = !form.hidden;
  e.currentTarget.setAttribute('aria-pressed', String(!form.hidden));
  if (!form.hidden) {
    $('set-engine').value = state.settings.searchEngine;
    $('set-city').value = state.settings.city;
    $('set-status').textContent = '';
  }
});

$('settings').addEventListener('submit', async (e) => {
  e.preventDefault();
  const status = $('set-status');
  try {
    const cityChanged = $('set-city').value.trim() !== state.settings.city;
    state.settings = await api('/api/settings', {
      method: 'PUT',
      body: { searchEngine: $('set-engine').value, city: $('set-city').value },
    });
    updateHint();
    if (cityChanged) loadWeather();
    status.textContent = 'Сохранено';
  } catch (ex) {
    if (!ignorable(ex)) status.textContent = ex.message;
  }
});

// ---------- search & suggestions ----------
const q = $('q');
const sugList = $('suggestions');
let suggestTimer = null;
let suggestCtrl = null;
let active = -1;

function updateHint() {
  const r = resolveQuery(q.value);
  $('engine-hint').textContent = r ? r.label : (ENGINES[state.settings.searchEngine] || ENGINES.g).name;
}

function hideSuggestions() {
  sugList.hidden = true;
  sugList.textContent = '';
  active = -1;
  q.setAttribute('aria-expanded', 'false');
  q.removeAttribute('aria-activedescendant');
}

function renderSuggestions(items, prefix) {
  sugList.textContent = '';
  active = -1;
  if (!items.length) { hideSuggestions(); return; }
  items.forEach((text, i) => {
    const li = el('li', null, text);
    li.id = 'sug-' + i;
    li.setAttribute('role', 'option');
    li.dataset.value = prefix ? `${prefix} ${text}` : text;
    // mousedown, not click: it fires before the input's blur hides the list.
    li.addEventListener('mousedown', (e) => {
      e.preventDefault();
      q.value = li.dataset.value;
      go();
    });
    sugList.append(li);
  });
  sugList.hidden = false;
  q.setAttribute('aria-expanded', 'true');
}

function highlight(i) {
  const items = sugList.children;
  if (!items.length) return;
  active = (i + items.length) % items.length;
  [...items].forEach((li, j) => li.setAttribute('aria-selected', String(j === active)));
  q.value = items[active].dataset.value;
  q.setAttribute('aria-activedescendant', items[active].id);
  updateHint();
}

async function fetchSuggestions() {
  const raw = q.value.trim();
  const { prefix, text } = parsePrefix(raw);
  if (!text || looksLikeAddress(raw)) { hideSuggestions(); return; }
  if (suggestCtrl) suggestCtrl.abort();
  suggestCtrl = new AbortController();
  try {
    const r = await api('/api/suggest?q=' + encodeURIComponent(text), { signal: suggestCtrl.signal });
    if (q.value.trim() !== raw) return; // the user kept typing while we waited
    renderSuggestions(r.suggestions, prefix);
  } catch (e) {
    if (!ignorable(e)) hideSuggestions();
  }
}

function go() {
  const r = resolveQuery(q.value);
  hideSuggestions();
  if (r) location.href = r.url;
}

q.addEventListener('input', () => {
  updateHint();
  clearTimeout(suggestTimer);
  suggestTimer = setTimeout(fetchSuggestions, 150);
});

q.addEventListener('keydown', (e) => {
  if (sugList.hidden) return;
  if (e.key === 'ArrowDown') { e.preventDefault(); highlight(active + 1); }
  else if (e.key === 'ArrowUp') { e.preventDefault(); highlight(active - 1); }
  else if (e.key === 'Escape') { hideSuggestions(); }
});

q.addEventListener('blur', () => setTimeout(hideSuggestions, 100));

$('search').addEventListener('submit', (e) => {
  e.preventDefault();
  go();
});

// ---------- tasks ----------
const isDemo = () => document.body.classList.contains('demo');

function taskItem(t) {
  const li = el('li', t.done ? 'done' : '');
  const label = el('label');
  const box = el('input');
  box.type = 'checkbox';
  box.checked = t.done;
  box.disabled = isDemo();
  box.addEventListener('change', async () => {
    box.disabled = true;
    try {
      await api(`/api/tasks/${t.id}`, { method: 'PATCH', body: { done: box.checked } });
      $('tasks-error').textContent = '';
      loadTasks();
    } catch (e) {
      box.checked = !box.checked;
      box.disabled = false;
      if (!ignorable(e)) $('tasks-error').textContent = e.message;
    }
  });
  label.append(box, el('span', 'title', t.title));
  const rm = el('button', 'del edit-only', '×');
  rm.type = 'button';
  rm.title = 'Удалить';
  rm.setAttribute('aria-label', 'Удалить задачу: ' + t.title);
  rm.addEventListener('click', async () => {
    try {
      await api(`/api/tasks/${t.id}`, { method: 'DELETE' });
      loadTasks();
    } catch (e) {
      if (!ignorable(e)) $('tasks-error').textContent = e.message;
    }
  });
  li.append(label, rm);
  return li;
}

async function loadTasks() {
  try {
    const list = await api('/api/tasks');
    const ul = $('tasks');
    ul.textContent = '';
    if (!list.length) ul.append(el('li', 'empty', 'Задач нет'));
    list.forEach((t) => ul.append(taskItem(t)));
    const done = list.filter((t) => t.done).length;
    $('tasks-clear').hidden = done === 0;
    $('tasks-clear').textContent = `Убрать выполненные (${done})`;
  } catch (e) {
    if (!ignorable(e)) $('tasks-error').textContent = 'Задачи не загрузились: ' + e.message;
  }
}

$('add-task').addEventListener('submit', async (e) => {
  e.preventDefault();
  const input = $('task-title');
  const title = input.value.trim();
  if (!title) return;
  input.disabled = true;
  try {
    await api('/api/tasks', { method: 'POST', body: { title } });
    $('tasks-error').textContent = '';
    input.value = '';
    loadTasks();
  } catch (ex) {
    if (!ignorable(ex)) $('tasks-error').textContent = ex.message;
  } finally {
    input.disabled = false;
    input.focus();
  }
});

$('tasks-clear').addEventListener('click', async () => {
  try {
    await api('/api/tasks/completed', { method: 'DELETE' });
    loadTasks();
  } catch (e) {
    if (!ignorable(e)) $('tasks-error').textContent = e.message;
  }
});

// ---------- note ----------
// Saves itself as you type. Every new tab is a start page, so several copies
// are often open: the note is re-read when a tab comes back, and the last save
// wins. Two rules keep typed text safe:
// - one save in flight at a time, so an older save can't land after a newer one;
// - text from the server never replaces the textarea while there are edits it
//   hasn't seen, which would also move the cursor.
const noteEl = $('note');
const note = { edits: 0, savedEdits: 0, inFlight: false, again: false, timer: 0 };

function fmtSaved(iso) {
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay
    ? d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString('ru-RU', { day: 'numeric', month: 'short' });
}

const noteStatus = (text) => { $('note-status').textContent = text; };
const noteDirty = () => note.edits !== note.savedEdits || note.inFlight;

async function loadNote() {
  const before = note.edits;
  try {
    const n = await api('/api/note');
    if (note.edits !== before || noteDirty()) return; // typed meanwhile: keep the local text
    if (noteEl.value !== n.text) noteEl.value = n.text;
    noteStatus(n.text ? 'сохранено ' + fmtSaved(n.updatedAt) : '');
  } catch (e) {
    if (!ignorable(e)) noteStatus('не загрузилась: ' + e.message);
  }
}

async function saveNote({ keepalive = false } = {}) {
  clearTimeout(note.timer);
  if (note.inFlight) { note.again = true; return; }
  if (note.edits === note.savedEdits) return;
  const edits = note.edits;
  note.inFlight = true;
  noteStatus('сохраняю…');
  let ok = false;
  try {
    const saved = await api('/api/note', { method: 'PUT', body: { text: noteEl.value }, keepalive });
    note.savedEdits = edits;
    ok = true;
    if (note.edits === edits) noteStatus('сохранено ' + fmtSaved(saved.updatedAt));
  } catch (e) {
    if (!ignorable(e)) noteStatus('не сохранилось: ' + e.message);
    // A network or server error may pass; a rejected text won't, so don't loop on it.
    if (!(e instanceof AuthError) && !(e.status >= 400 && e.status < 500)) {
      note.timer = setTimeout(saveNote, 5000);
    }
  } finally {
    note.inFlight = false;
  }
  const again = note.again;
  note.again = false;
  if (ok && (again || note.edits !== note.savedEdits)) saveNote();
}

noteEl.addEventListener('input', () => {
  note.edits++;
  noteStatus('');
  clearTimeout(note.timer);
  note.timer = setTimeout(saveNote, 700);
});
noteEl.addEventListener('blur', () => saveNote());

document.addEventListener('visibilitychange', () => {
  if ($('app').hidden) return;
  if (document.visibilityState === 'hidden') {
    saveNote({ keepalive: true });
  } else {
    // Another tab may have changed both while this one was in the background.
    loadNote();
    loadTasks();
  }
});

// ---------- background ----------
// Painting of the day. The server proxies the image, so the page still talks
// only to its own origin. Any failure keeps the plain background.
//
// A new tab must open looking right, not plain and then repainted: the last
// painting is remembered and shown at once (the browser has its image cached
// for the day), and /api/art is then asked whether a new one is due.
const ART_KEY = 'startpage.art';

function showArt(art, { instant }) {
  const img = $('art-img');
  img.parentElement.classList.toggle('instant', instant);
  img.onload = () => img.classList.add('shown');
  img.src = art.image;
  const caption = $('art-caption');
  const work = [art.title, art.date].filter(Boolean).join(', ');
  caption.textContent = [art.artist, work].filter(Boolean).join(' — ');
  const href = art.url && safeUrl(art.url);
  if (href) caption.href = href; else caption.removeAttribute('href');
  // Long titles are cut off in the corner; the tooltip has them in full.
  caption.title = caption.textContent + '\nCleveland Museum of Art, Open Access (CC0)';
  caption.hidden = false;
  document.body.classList.add('has-art');
}

function restoreArt() {
  let art = null;
  try { art = JSON.parse(localStorage.getItem(ART_KEY)); } catch { /* no storage: load as usual */ }
  if (art && typeof art.image === 'string' && art.image.startsWith('/api/art/image?v=')) {
    showArt(art, { instant: true });
  }
}

async function loadArt() {
  try {
    const art = await api('/api/art');
    if ($('art-img').getAttribute('src') === art.image) return;
    // Decode off-screen first: a failed download must not replace a good painting.
    const next = new Image();
    next.src = art.image;
    await next.decode();
    // Fade in only the very first painting; replacing one is a plain swap.
    showArt(art, { instant: document.body.classList.contains('has-art') });
    const { image, title, artist, date, url } = art;
    try { localStorage.setItem(ART_KEY, JSON.stringify({ image, title, artist, date, url })); } catch { /* not remembered */ }
  } catch { /* plain background */ }
}

function clearArt() {
  document.body.classList.remove('has-art');
  $('art-caption').hidden = true;
  const img = $('art-img');
  img.classList.remove('shown');
  img.removeAttribute('src');
  try { localStorage.removeItem(ART_KEY); } catch { /* nothing stored */ }
}

// ---------- boot ----------
// Demo mode hides edit controls; the server refuses writes either way.
async function loadMeta() {
  try {
    const meta = await api('/api/meta');
    document.body.classList.toggle('demo', meta.demo);
    $('demo-banner').hidden = !meta.demo;
    noteEl.readOnly = meta.demo;
  } catch { /* unknown mode: show the normal UI; a demo server still refuses writes */ }
}

async function boot() {
  await loadMeta();
  try {
    state.settings = await api('/api/settings');
  } catch (e) {
    if (e instanceof AuthError) return;
    // The API failed but the page itself loaded: show it with defaults.
  }
  showApp();
  updateHint();
  loadWeather();
  loadRates();
  loadBookmarks();
  loadTasks();
  loadNote();
  loadServices();
  loadArt();
  stopTimers();
  every(loadArt, 60 * 60 * 1000); // a tab left open overnight gets the next day's painting
  every(loadWeather, 10 * 60 * 1000);
  every(loadRates, 60 * 60 * 1000);
  every(loadServices, 30 * 1000);
}

tick();
setInterval(tick, 1000);
restoreArt(); // before boot's API calls, so the first paint already has the painting
boot();
