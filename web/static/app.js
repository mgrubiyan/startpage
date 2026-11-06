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

async function api(path, { method = 'GET', body, signal } = {}) {
  const init = { method, signal, credentials: 'same-origin', headers: {} };
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
  } catch (e) {
    if (ignorable(e)) return;
    $('weather-city').textContent = state.settings.city || 'Погода';
    $('weather-value').textContent = '—';
    $('weather').title = 'Погода недоступна: ' + e.message;
  }
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

// ---------- boot ----------
async function boot() {
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
  loadServices();
  stopTimers();
  every(loadWeather, 10 * 60 * 1000);
  every(loadRates, 60 * 60 * 1000);
  every(loadServices, 30 * 1000);
}

tick();
setInterval(tick, 1000);
boot();
