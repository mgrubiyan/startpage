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

// ---------- last-known data ----------
// Every new tab is this page, so it must open whole, not fill itself in block
// by block and push the rest down. Each widget's last answer is kept and drawn
// before any request; the fresh one replaces it only if it differs. The note is
// left out on purpose: an old copy in the textarea could be saved over a newer
// text. Logging out, or the session running out, forgets it all.
const LAST = 'startpage.last.';
const LAST_NAMES = ['meta', 'settings', 'weather', 'rates', 'markets', 'bookmarks', 'tasks', 'services'];
const drawn = {}; // widget → JSON of what it shows now

function recall(name) {
  try { return JSON.parse(localStorage.getItem(LAST + name)); } catch { return null; }
}

function remember(name, data) {
  try { localStorage.setItem(LAST + name, JSON.stringify(data)); } catch { /* not kept: the next tab loads as usual */ }
}

function forgetAll() {
  LAST_NAMES.forEach((name) => {
    delete drawn[name];
    try { localStorage.removeItem(LAST + name); } catch { /* nothing kept */ }
  });
}

// show draws a widget unless that exact data is on screen already: polls
// mostly bring the same answer, and redrawing it would reset the hover, an
// open tooltip and the favicons. A fresh answer is also kept for the next tab.
function show(name, data, render, { keep = true } = {}) {
  const key = JSON.stringify(data);
  if (drawn[name] === key) return;
  drawn[name] = key;
  if (keep) {
    try { localStorage.setItem(LAST + name, key); } catch { /* not kept */ }
  }
  render(data);
}

// After an error message replaced a widget, its next answer must be drawn.
const undrawn = (name) => { delete drawn[name]; };

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
  forgetAll(); // nor does the next person at this browser get the last page
  $('app').hidden = true;
  $('login').hidden = false;
  $('login-password').focus();
}

function showApp() {
  // restore() may have shown it already, and a second focus() would pull the
  // cursor out of whatever field the user went to in the meantime.
  if (!$('app').hidden) return;
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

function renderWeather(w) {
  $('weather-city').textContent = w.location;
  $('weather-value').textContent = `${w.icon} ${fmtTemp(w.temperature)} · ${Math.round(w.windMs)} м/с`;
  $('weather').title = `${w.description}, ощущается как ${fmtTemp(w.feelsLike)}` + (w.stale ? ' · данные устарели' : '');
  renderForecast(w.days || [], w.stale);
}

async function loadWeather() {
  try {
    show('weather', await api('/api/weather'), renderWeather);
  } catch (e) {
    if (ignorable(e)) return;
    undrawn('weather');
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

// ---------- markets ----------
const RUB = ' ₽';

function fmtPrice(q) {
  // Big prices read better without kopecks/cents; small coins need them.
  const digits = q.price >= 1000 ? 0 : q.price >= 1 ? 2 : 4;
  const s = q.price.toLocaleString('ru-RU', { minimumFractionDigits: digits, maximumFractionDigits: digits });
  if (q.currency === 'RUB') return s + RUB;
  if (q.currency === 'USDT') return '$' + s;
  return s; // an index, in points
}

function fmtChange(pct) {
  const r = Math.round(pct * 100) / 100;
  if (r === 0) return { text: '0,00%', cls: '' };
  return {
    text: (r > 0 ? '+' : '−') + Math.abs(r).toLocaleString('ru-RU', { minimumFractionDigits: 2, maximumFractionDigits: 2 }) + '%',
    cls: r > 0 ? 'pos' : 'neg',
  };
}

function marketBoard(title, b) {
  const box = el('div', 'market' + (b.stale ? ' stale' : ''));
  const head = el('div', 'market-head');
  const src = [b.source, b.delayMinutes ? `задержка ${b.delayMinutes} мин` : ''].filter(Boolean).join(' · ');
  head.append(el('span', null, title), el('span', 'src', src));
  const ul = el('ul', 'quotes');
  if (b.error || !b.quotes.length) ul.append(el('li', 'empty', b.error ? 'Биржа недоступна' : 'Нет котировок'));
  b.quotes.forEach((q) => {
    const li = el('li');
    const who = el('span', 'who');
    who.append(el('span', 'sym', q.symbol), el('span', 'name', q.name));
    const chg = fmtChange(q.change);
    li.append(who, el('span', 'price', fmtPrice(q)), el('span', 'chg ' + chg.cls, chg.text));
    const at = q.at ? new Date(q.at).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }) : '';
    li.title = [q.name, at && 'цена на ' + at, b.stale && 'данные устарели'].filter(Boolean).join('\n');
    ul.append(li);
  });
  box.append(head, ul);
  return box;
}

function renderMarkets(m) {
  const root = $('markets');
  root.textContent = '';
  if (!m.available) { root.append(el('p', 'note-line', m.reason || 'Котировки недоступны')); return; }
  if (m.crypto) root.append(marketBoard('Криптовалюты', m.crypto));
  if (m.stocks) root.append(marketBoard('Акции', m.stocks));
}

async function loadMarkets() {
  const root = $('markets');
  try {
    show('markets', await api('/api/markets'), renderMarkets);
  } catch (e) {
    if (ignorable(e)) return;
    undrawn('markets');
    root.textContent = '';
    root.append(el('p', 'note-line', 'Биржи недоступны: ' + e.message));
  }
}

// ---------- rates ----------
function renderRates(r) {
  const box = $('rates-value');
  box.textContent = '';
  [['$', r.usd], ['€', r.eur]].forEach(([sym, v], i) => {
    if (i) box.append(' ');
    box.append(`${sym} ${v.value.toFixed(2)}`);
    const diff = v.value - v.previous;
    if (Math.abs(diff) >= 0.005) box.append(el('span', diff > 0 ? 'up' : 'down', diff > 0 ? '▲' : '▼'));
  });
  $('rates').title = 'Курс ЦБ РФ на ' + new Date(r.date).toLocaleDateString('ru-RU') + (r.stale ? ' · данные устарели' : '');
}

async function loadRates() {
  try {
    show('rates', await api('/api/rates'), renderRates);
  } catch (e) {
    if (ignorable(e)) return;
    undrawn('rates');
    $('rates-value').textContent = '—';
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

function renderBookmarks(items) {
  const ul = $('bookmarks');
  ul.textContent = '';
  items.forEach((bm) => {
    const tile = bookmarkTile(bm);
    if (tile) ul.append(tile);
  });
}

async function loadBookmarks() {
  try {
    show('bookmarks', await api('/api/bookmarks'), renderBookmarks);
    $('bm-error').textContent = '';
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
// The demo's own health check answers in under a millisecond; "0 мс" would read as broken.
const fmtMs = (ms) => (ms < 1 ? '<1 мс' : `${ms} мс`);
const fmtPct = (p) => p.toFixed(Number.isInteger(p) ? 0 : 1);

function serviceMeta(s) {
  const c = s.lastCheck;
  if (!c) return 'ещё не проверялся';
  const latency = fmtMs(c.latencyMs);
  const head = c.ok ? latency : (c.statusCode ? `HTTP ${c.statusCode}` : 'недоступен');
  if (s.uptime == null) return head;
  return `${head} · ${fmtPct(s.uptime)}% за сутки`;
}

const hhmm = (d) => d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });

// A day of hourly slots, oldest first, like a status page: green when every
// check passed, amber when some failed, red when none passed, grey when unchecked.
function historyBars(history, uptime) {
  const row = el('div', 'bars');
  row.setAttribute('role', 'img');
  // The same daily figure the row shows, not one recomputed over whole hours.
  row.setAttribute('aria-label', uptime != null
    ? `Аптайм по часам за сутки, всего ${fmtPct(uptime)}%`
    : 'Аптайм по часам за сутки: проверок не было');
  history.forEach((h) => {
    const start = new Date(h.start);
    const end = new Date(start.getTime() + 60 * 60 * 1000);
    const cls = !h.total ? '' : h.ok === h.total ? 'ok' : h.ok === 0 ? 'down' : 'part';
    const bar = el('i', cls);
    const span = `${hhmm(start)}–${hhmm(end)}`;
    if (!h.total) {
      bar.title = `${span} · не проверялся`;
    } else {
      const pct = Math.round((h.ok / h.total) * 1000) / 10;
      bar.title = `${span} · ${pct}% (${h.ok} из ${h.total})` + (h.ok ? ` · ${fmtMs(h.latencyMs)}` : '');
    }
    row.append(bar);
  });
  return row;
}

function renderServices(items) {
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
    if (s.history && s.history.length) li.append(historyBars(s.history, s.uptime));
    ul.append(li);
  });
}

async function loadServices() {
  try {
    // Polled every 30s while the monitor checks about once a minute, so most
    // answers are the same one: show() skips those.
    show('services', await api('/api/services'), renderServices);
  } catch (e) {
    if (ignorable(e)) return;
    undrawn('services'); // the message replaced the list: draw it again next time
    $('services').textContent = 'Статусы не загрузились: ' + e.message;
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
    remember('settings', state.settings);
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

let tasks = [];       // as listed on screen
let tasksSorted = []; // as the server lists them now

// moveTask puts a task that changed where the server now lists it: open tasks
// oldest first, then done ones, the one done last at the top.
function moveTask(list, saved) {
  const rest = list.filter((t) => t.id !== saved.id);
  const open = rest.filter((t) => !t.done);
  const done = rest.filter((t) => t.done);
  return saved.done
    ? [...open, saved, ...done]
    : [...[...open, saved].sort((a, b) => a.id - b.id), ...done];
}

function updateTasksClear() {
  const done = tasks.filter((t) => t.done).length;
  $('tasks-clear').hidden = done === 0;
  $('tasks-clear').textContent = `Убрать выполненные (${done})`;
}

// A checked task stays where it is, under the cursor, just crossed out: moving
// it to the done ones at once would make the list jump on every click. The list
// takes the server's order on its next load, e.g. when the tab is shown again.
function taskDone(li, saved) {
  li.classList.toggle('done', saved.done);
  tasks = tasks.map((t) => (t.id === saved.id ? saved : t));
  drawn.tasks = JSON.stringify(tasks); // so the next load, sorted, differs and is drawn
  tasksSorted = moveTask(tasksSorted, saved);
  remember('tasks', tasksSorted); // a new tab opens in the server's order straight away
  updateTasksClear();
}

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
      const saved = await api(`/api/tasks/${t.id}`, { method: 'PATCH', body: { done: box.checked } });
      $('tasks-error').textContent = '';
      box.checked = saved.done;
      box.disabled = false;
      taskDone(li, saved);
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

function renderTasks(list) {
  tasks = list;
  tasksSorted = list; // both the server and the kept copy send them sorted
  const ul = $('tasks');
  ul.textContent = '';
  if (!list.length) ul.append(el('li', 'empty', 'Задач нет'));
  list.forEach((t) => ul.append(taskItem(t)));
  updateTasksClear();
}

async function loadTasks() {
  try {
    show('tasks', await api('/api/tasks'), renderTasks);
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
  // A save takes some tens of milliseconds: saying so would only flash a word.
  // Only a slow one gets a status, so the user knows it is still going.
  const slow = setTimeout(() => noteStatus('сохраняю…'), 400);
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
    clearTimeout(slow);
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
    loadMarkets();
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
function applyMeta(meta) {
  document.body.classList.toggle('demo', meta.demo);
  $('demo-banner').hidden = !meta.demo;
  noteEl.readOnly = meta.demo;
}

async function loadMeta() {
  try {
    const meta = await api('/api/meta');
    applyMeta(meta);
    remember('meta', meta);
  } catch { /* unknown mode: show the normal UI; a demo server still refuses writes */ }
}

// restore draws the last page this browser saw, before any request, so a new
// tab opens whole and boot() then only corrects what changed. Settings are kept
// only once they were loaded with a valid session, so a browser that never
// logged in, or logged out, gets the login screen as before.
const WIDGETS = {
  weather: renderWeather, rates: renderRates, markets: renderMarkets,
  bookmarks: renderBookmarks, tasks: renderTasks, services: renderServices,
};

function restore() {
  const settings = recall('settings');
  if (!settings) return;
  state.settings = settings;
  const meta = recall('meta');
  if (meta) applyMeta(meta);
  Object.entries(WIDGETS).forEach(([name, render]) => {
    const data = recall(name);
    if (data == null) return;
    try {
      show(name, data, render, { keep: false });
    } catch {
      // Kept by an older version of the page in another shape: let boot() draw it.
      undrawn(name);
      try { localStorage.removeItem(LAST + name); } catch { /* nothing kept */ }
    }
  });
  showApp();
  updateHint();
}

async function boot() {
  await loadMeta();
  try {
    state.settings = await api('/api/settings');
    remember('settings', state.settings);
  } catch (e) {
    if (e instanceof AuthError) return;
    // The API failed but the page itself loaded: show it with defaults.
  }
  showApp();
  updateHint();
  loadWeather();
  loadRates();
  loadMarkets();
  loadBookmarks();
  loadTasks();
  loadNote();
  loadServices();
  loadArt();
  stopTimers();
  every(loadArt, 60 * 60 * 1000); // a tab left open overnight gets the next day's painting
  every(loadWeather, 10 * 60 * 1000);
  every(loadRates, 60 * 60 * 1000);
  // Prices move by the minute, but a tab in the background needn't keep asking.
  every(() => { if (document.visibilityState === 'visible') loadMarkets(); }, 60 * 1000);
  every(loadServices, 30 * 1000);
}

tick();
setInterval(tick, 1000);
restoreArt(); // before boot's API calls, so the first paint already has the painting
restore();
boot();
