'use strict';

// ---------- storage ----------
const KEY = 'startpage.v1';
const RATES_KEY = 'startpage.rates';

const ENGINES = {
  g:  { name: 'Google',     url: 'https://www.google.com/search?q=' },
  y:  { name: 'Яндекс',     url: 'https://yandex.ru/search/?text=' },
  d:  { name: 'DuckDuckGo', url: 'https://duckduckgo.com/?q=' },
  gh: { name: 'GitHub',     url: 'https://github.com/search?type=repositories&q=' },
  yt: { name: 'YouTube',    url: 'https://www.youtube.com/results?search_query=' },
  w:  { name: 'Википедия',  url: 'https://ru.wikipedia.org/w/index.php?search=' },
};

const DEFAULTS = {
  engine: 'g',
  city: 'Санкт-Петербург',
  geo: null,
  bookmarks: [
    { title: 'GitHub',   url: 'https://github.com' },
    { title: 'Claude',   url: 'https://claude.ai' },
    { title: 'YouTube',  url: 'https://www.youtube.com' },
    { title: 'Gmail',    url: 'https://mail.google.com' },
    { title: 'Telegram', url: 'https://web.telegram.org' },
    { title: 'Карты',    url: 'https://yandex.ru/maps' },
  ],
  services: [],
};

function readJSON(key) {
  try { return JSON.parse(localStorage.getItem(key)); } catch { return null; }
}
function writeJSON(key, value) {
  try { localStorage.setItem(key, JSON.stringify(value)); } catch { /* storage blocked: work in-memory */ }
}

const state = Object.assign({}, DEFAULTS, readJSON(KEY) || {});
if (!ENGINES[state.engine]) state.engine = DEFAULTS.engine;
if (!Array.isArray(state.bookmarks)) state.bookmarks = DEFAULTS.bookmarks.slice();
if (!Array.isArray(state.services)) state.services = [];
const save = () => writeJSON(KEY, state);

const $ = (id) => document.getElementById(id);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

// Only http(s) URLs are allowed anywhere a link is rendered or opened.
function safeUrl(raw) {
  try {
    const u = new URL(raw);
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : null;
  } catch { return null; }
}

// ---------- widget: clock ----------
function clockWidget() {
  const time = $('time');
  const date = $('date');
  const tick = () => {
    const now = new Date();
    time.textContent = now.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
    const d = now.toLocaleDateString('ru-RU', { weekday: 'long', day: 'numeric', month: 'long' });
    date.textContent = d.charAt(0).toUpperCase() + d.slice(1);
  };
  tick();
  setInterval(tick, 1000);
}

// ---------- widget: search ----------
// "gh query" -> GitHub search; plain text -> default engine; things that look like an address open directly.
function looksLikeAddress(s) {
  if (/\s/.test(s)) return null;
  if (/^https?:\/\//i.test(s)) return safeUrl(s);
  if (/^(localhost|\d{1,3}(\.\d{1,3}){3})(:\d+)?(\/\S*)?$/i.test(s)) return safeUrl('http://' + s);
  if (/^[\w-]+(\.[\w-]+)*\.[a-zа-я]{2,}(:\d+)?([/?#]\S*)?$/i.test(s)) return safeUrl('https://' + s);
  return null;
}

function resolveQuery(raw) {
  const s = raw.trim();
  if (!s) return null;
  const m = s.match(/^(\S+)\s+(.+)$/);
  if (m && ENGINES[m[1].toLowerCase()]) {
    const e = ENGINES[m[1].toLowerCase()];
    return { url: e.url + encodeURIComponent(m[2].trim()), label: e.name };
  }
  const addr = looksLikeAddress(s);
  if (addr) return { url: addr, label: 'Открыть' };
  const e = ENGINES[state.engine];
  return { url: e.url + encodeURIComponent(s), label: e.name };
}

function searchWidget() {
  const form = $('search');
  const q = $('q');
  const hint = $('engine-hint');
  const updateHint = () => {
    const r = resolveQuery(q.value);
    hint.textContent = r ? r.label : ENGINES[state.engine].name;
  };
  q.addEventListener('input', updateHint);
  form.addEventListener('submit', (e) => {
    e.preventDefault();
    const r = resolveQuery(q.value);
    if (r) location.href = r.url;
  });
  updateHint();
  q.focus();
  return { refresh: updateHint };
}

// ---------- widget: weather (Open-Meteo, no key) ----------
const WMO = [
  [[0], '☀️', 'ясно'],
  [[1, 2], '🌤️', 'малооблачно'],
  [[3], '☁️', 'пасмурно'],
  [[45, 48], '🌫️', 'туман'],
  [[51, 53, 55, 56, 57], '🌦️', 'морось'],
  [[61, 63, 65, 66, 67, 80, 81, 82], '🌧️', 'дождь'],
  [[71, 73, 75, 77, 85, 86], '🌨️', 'снег'],
  [[95, 96, 99], '⛈️', 'гроза'],
];
function describeWeather(code) {
  const hit = WMO.find(([codes]) => codes.includes(code));
  return hit ? { icon: hit[1], text: hit[2] } : { icon: '🌡️', text: '' };
}

async function fetchJSON(url) {
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), 8000);
  try {
    const res = await fetch(url, { signal: ctrl.signal });
    if (!res.ok) throw new Error('HTTP ' + res.status);
    return await res.json();
  } finally {
    clearTimeout(t);
  }
}

async function geocode(city) {
  if (state.geo && state.geo.city === city) return state.geo;
  const data = await fetchJSON('https://geocoding-api.open-meteo.com/v1/search?count=1&language=ru&name=' + encodeURIComponent(city));
  const r = data.results && data.results[0];
  if (!r) throw new Error('город не найден');
  state.geo = { city, lat: r.latitude, lon: r.longitude, name: r.name };
  save();
  return state.geo;
}

async function loadWeather() {
  const label = $('weather-city');
  const value = $('weather-value');
  label.textContent = state.city;
  value.textContent = '…';
  try {
    const geo = await geocode(state.city);
    label.textContent = geo.name;
    const data = await fetchJSON(
      `https://api.open-meteo.com/v1/forecast?latitude=${geo.lat}&longitude=${geo.lon}` +
      '&current=temperature_2m,apparent_temperature,weather_code,wind_speed_10m&wind_speed_unit=ms&timezone=auto'
    );
    const c = data.current;
    const w = describeWeather(c.weather_code);
    const temp = Math.round(c.temperature_2m);
    value.textContent = `${w.icon} ${temp > 0 ? '+' : ''}${temp}° · ${Math.round(c.wind_speed_10m)} м/с`;
    $('weather').title = `${w.text}, ощущается как ${Math.round(c.apparent_temperature)}°`;
  } catch (err) {
    value.textContent = '—';
    $('weather').title = 'Погода недоступна: ' + err.message;
  }
}

function weatherWidget() {
  loadWeather();
  setInterval(loadWeather, 30 * 60 * 1000);
  return { refresh: loadWeather };
}

// ---------- widget: CBR rates ----------
function renderRates(r) {
  const box = $('rates-value');
  box.textContent = '';
  [['$', r.usd], ['€', r.eur]].forEach(([sym, v], i) => {
    if (i) box.append(' ');
    box.append(`${sym} ${v.value.toFixed(2)}`);
    const diff = v.value - v.prev;
    if (Math.abs(diff) >= 0.005) box.append(el('span', diff > 0 ? 'up' : 'down', diff > 0 ? '▲' : '▼'));
  });
  $('rates').title = 'Курс ЦБ РФ на ' + new Date(r.date).toLocaleDateString('ru-RU');
}

async function loadRates() {
  const cached = readJSON(RATES_KEY);
  if (cached) renderRates(cached);
  try {
    const d = await fetchJSON('https://www.cbr-xml-daily.ru/daily_json.js');
    const pick = (c) => ({ value: d.Valute[c].Value, prev: d.Valute[c].Previous });
    const r = { date: d.Date, usd: pick('USD'), eur: pick('EUR') };
    writeJSON(RATES_KEY, r);
    renderRates(r);
  } catch (err) {
    if (!cached) $('rates-value').textContent = '—';
    $('rates').title = 'Курсы недоступны: ' + err.message;
  }
}

function ratesWidget() {
  loadRates();
  setInterval(loadRates, 60 * 60 * 1000);
}

// ---------- widget: bookmarks ----------
function hue(str) {
  let h = 0;
  for (const ch of str) h = (h * 31 + ch.codePointAt(0)) % 360;
  return h;
}

function bookmarkTile(bm, index) {
  const url = safeUrl(bm.url);
  if (!url) return null;
  const host = new URL(url).hostname;
  const li = el('li');
  const a = el('a');
  a.href = url;
  const icon = el('span', 'icon');
  icon.style.background = `hsl(${hue(host)} 55% 50%)`;
  icon.textContent = (bm.title || host).trim().charAt(0).toUpperCase();
  const img = el('img');
  img.alt = '';
  img.src = 'https://www.google.com/s2/favicons?sz=64&domain=' + encodeURIComponent(host);
  img.addEventListener('load', () => { icon.textContent = ''; icon.style.background = 'transparent'; icon.append(img); });
  a.append(icon, el('span', 'label', bm.title || host));
  const rm = el('button', 'remove', '×');
  rm.type = 'button';
  rm.title = 'Удалить';
  rm.addEventListener('click', () => {
    state.bookmarks.splice(index, 1);
    save();
    renderBookmarks();
  });
  li.append(a, rm);
  return li;
}

function renderBookmarks() {
  const list = $('bookmarks');
  list.textContent = '';
  state.bookmarks.forEach((bm, i) => {
    const tile = bookmarkTile(bm, i);
    if (tile) list.append(tile);
  });
}

function bookmarksWidget() {
  const list = $('bookmarks');
  const toggle = $('edit-toggle');
  const form = $('add-bookmark');
  const err = $('bm-error');
  renderBookmarks();

  toggle.addEventListener('click', () => {
    const on = !list.classList.contains('editing');
    list.classList.toggle('editing', on);
    form.hidden = !on;
    toggle.setAttribute('aria-pressed', String(on));
    toggle.textContent = on ? 'Готово' : 'Изменить';
    if (on) $('bm-title').focus();
  });

  form.addEventListener('submit', (e) => {
    e.preventDefault();
    const title = $('bm-title').value.trim();
    const raw = $('bm-url').value.trim();
    const url = safeUrl(raw) || looksLikeAddress(raw);
    if (!url) { err.textContent = 'Нужен адрес вида https://example.com'; return; }
    err.textContent = '';
    state.bookmarks.push({ title, url });
    save();
    renderBookmarks();
    form.reset();
    $('bm-title').focus();
  });
}

// ---------- widget: services status ----------
// no-cors fetch: resolves if the host answered at all, rejects on network error / timeout.
// Good enough for "alive or not" without CORS headers on the target.
async function ping(url) {
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), 6000);
  const start = performance.now();
  try {
    await fetch(url, { mode: 'no-cors', cache: 'no-store', signal: ctrl.signal });
    return { ok: true, ms: Math.round(performance.now() - start) };
  } catch {
    return { ok: false };
  } finally {
    clearTimeout(t);
  }
}

function renderServices() {
  const list = $('services');
  list.textContent = '';
  if (!state.services.length) {
    list.append(el('li', 'empty', 'Пока пусто. Добавь адреса ботов в «Настройках», когда появится сервер.'));
    return;
  }
  state.services.forEach((s) => {
    const li = el('li');
    const dot = el('span', 'dot');
    const meta = el('span', 'meta', '…');
    li.append(dot, el('span', 'name', s.name), meta);
    list.append(li);
    ping(s.url).then((r) => {
      dot.classList.add(r.ok ? 'ok' : 'fail');
      meta.textContent = r.ok ? `${r.ms} мс` : 'недоступен';
    });
  });
}

function servicesWidget() {
  renderServices();
  setInterval(renderServices, 60 * 1000);
  return { refresh: renderServices };
}

function parseServices(text) {
  const out = [];
  const bad = [];
  text.split('\n').map((l) => l.trim()).filter(Boolean).forEach((line) => {
    const [name, raw] = line.includes('|') ? line.split('|').map((x) => x.trim()) : [null, line];
    const url = safeUrl(raw);
    if (url) out.push({ name: name || new URL(url).host, url });
    else bad.push(line);
  });
  return { out, bad };
}

// ---------- settings ----------
function settingsPanel(widgets) {
  const form = $('settings');
  const toggle = $('settings-toggle');
  const engine = $('set-engine');
  const city = $('set-city');
  const services = $('set-services');
  const status = $('set-status');

  Object.entries(ENGINES).forEach(([key, e]) => {
    const o = el('option', null, `${e.name} (${key})`);
    o.value = key;
    engine.append(o);
  });

  const fill = () => {
    engine.value = state.engine;
    city.value = state.city;
    services.value = state.services.map((s) => `${s.name} | ${s.url}`).join('\n');
    status.textContent = '';
  };

  toggle.addEventListener('click', () => {
    form.hidden = !form.hidden;
    toggle.setAttribute('aria-pressed', String(!form.hidden));
    if (!form.hidden) fill();
  });

  form.addEventListener('submit', (e) => {
    e.preventDefault();
    const { out, bad } = parseServices(services.value);
    if (bad.length) { status.textContent = 'Не понял строку: ' + bad[0]; return; }
    const newCity = city.value.trim() || DEFAULTS.city;
    const cityChanged = newCity !== state.city;
    state.engine = engine.value;
    state.city = newCity;
    state.services = out;
    save();
    widgets.search.refresh();
    widgets.services.refresh();
    if (cityChanged) widgets.weather.refresh();
    status.textContent = 'Сохранено';
  });
}

// ---------- boot ----------
clockWidget();
const widgets = {
  search: searchWidget(),
  weather: weatherWidget(),
  services: servicesWidget(),
};
ratesWidget();
bookmarksWidget();
settingsPanel(widgets);
