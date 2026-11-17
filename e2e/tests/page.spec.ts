import { expect, test } from '@playwright/test';

test('a checked task stays where it is until the list is next loaded', async ({ page }) => {
  // It asserts the whole list: start from an empty one, whatever ran before.
  for (const t of await (await page.request.get('/api/tasks')).json()) {
    await page.request.delete(`/api/tasks/${t.id}`);
  }
  await page.goto('/');
  const input = page.locator('#task-title');
  const titles = page.locator('#tasks .title');
  for (const title of ['Первая', 'Вторая', 'Третья']) {
    await input.fill(title);
    await input.press('Enter');
    await expect(titles.last()).toHaveText(title);
  }

  const first = page.locator('#tasks li', { hasText: 'Первая' });
  await first.getByRole('checkbox').check();
  await expect(first).toHaveClass(/done/);
  await expect(page.locator('#tasks-clear')).toHaveText('Убрать выполненные (1)');
  // Crossed out under the cursor, not moved away from it.
  await expect(titles).toHaveText(['Первая', 'Вторая', 'Третья']);

  // The next load lists it with the done ones, as the server orders them.
  await page.reload();
  await expect(titles).toHaveText(['Вторая', 'Третья', 'Первая']);
});

test('the note saves itself and is there after a reload', async ({ page }) => {
  await page.goto('/');
  const note = page.locator('#note');
  await expect(note).toBeEditable();
  await note.fill('купить молоко');
  await expect(page.locator('#note-status')).toHaveText(/^сохранено /);

  await page.reload();
  await expect(note).toHaveValue('купить молоко');
});

// The page draws itself from the last answers before the API is asked, but
// the note has no kept copy: an empty field mustn't be typed into and saved
// over the real text while it loads.
test('the note is locked until its text has loaded', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#note')).toBeEditable();

  let release: () => void = () => {};
  const held = new Promise<void>((resolve) => { release = resolve; });
  await page.route('**/api/note', async (route) => {
    if (route.request().method() === 'GET') await held;
    await route.continue();
  });
  await page.reload();
  await expect(page.locator('#app')).toBeVisible();
  await expect(page.locator('#note')).not.toBeEditable();

  release();
  await expect(page.locator('#note')).toBeEditable();
});

test('a new tab opens from the last answers, before the API replies', async ({ page }) => {
  // Its own bookmark and task, so it passes alone and again (--repeat-each).
  const title = `Пример ${Date.now()}`;
  await page.request.post('/api/bookmarks', { data: { title, url: 'https://example.com' } });
  await page.request.post('/api/tasks', { data: { title } });
  await page.goto('/');
  const tile = page.locator('#bookmarks li', { hasText: title });
  await expect(tile).toBeVisible();

  // Hold the bookmarks and tasks back: what shows now comes from the kept copy.
  let release: () => void = () => {};
  const held = new Promise<void>((resolve) => { release = resolve; });
  await page.route(/\/api\/(bookmarks|tasks)$/, async (route) => {
    await held;
    await route.continue();
  });
  await page.reload();
  await expect(tile).toBeVisible({ timeout: 1000 });
  await expect(page.locator('#tasks .title', { hasText: title })).toBeVisible({ timeout: 1000 });
  release();
});

test('coins are picked from the exchange list, and bookmarks open in a new tab', async ({ page }) => {
  // The exchanges' lists, stood in for: CI has no business with Binance.
  // They're held back until something is typed, as a slow exchange would.
  let release: () => void = () => {};
  const listed = new Promise<void>((resolve) => { release = resolve; });
  await page.route('**/api/markets/catalog', async (route) => {
    await listed;
    await route.fulfill({ json: {
      available: true,
      crypto: { items: [{ symbol: 'BTC', name: 'Bitcoin' }, { symbol: 'DOGE', name: 'Dogecoin' }, { symbol: 'DOT', name: 'Polkadot' }] },
      shares: { items: [], error: 'список недоступен' },
    } });
  });
  // Its own starting point, so it can run again (--repeat-each) on its own data.
  await page.request.put('/api/settings', {
    data: { crypto: ['BTC', 'ETH', 'SOL', 'TON'], shares: ['SBER', 'GAZP', 'LKOH', 'YDEX'] },
  });
  const title = `Вкладка ${Date.now()}`;
  await page.request.post('/api/bookmarks', { data: { title, url: 'https://example.com' } });
  await page.goto('/');

  const link = page.locator('#bookmarks a', { hasText: title });
  await expect(link).toHaveAttribute('target', '_blank');
  await expect(link).toHaveAttribute('rel', /noopener/);

  await page.locator('#markets-edit').click();
  const coins = page.getByRole('combobox', { name: 'Криптовалюты: добавить' });
  await expect(coins).toBeFocused();
  await coins.fill('dog');
  release(); // the lists arrive mid-word: what was typed stays, now with suggestions
  await expect(coins).toHaveValue('dog');
  await expect(page.getByRole('option')).toHaveText(['DOGEDogecoin']);
  await coins.press('Enter');

  const picked = page.locator('#markets-picker .picked').first();
  await expect(picked).toContainText('DOGE');
  await expect(coins).toBeFocused(); // keeps typing after a pick
  await page.getByRole('button', { name: 'Убрать BTC' }).click();
  await expect(picked).not.toContainText('BTC');

  // The shares list didn't load: a ticker typed in full still goes in.
  const shares = page.getByRole('combobox', { name: 'Акции: добавить' });
  await expect(page.locator('#markets-picker .pick-note').nth(1)).toContainText('вручную');
  await shares.fill('vtbr');
  await shares.press('Enter');
  await expect(page.locator('#markets-picker .picked').nth(1)).toContainText('VTBR');

  // Chips show before their saves finish: wait for the server to have both.
  const stored = async () => (await page.request.get('/api/settings')).json();
  await expect.poll(async () => (await stored()).shares).toContain('VTBR');
  await expect.poll(async () => (await stored()).crypto).toEqual(['ETH', 'SOL', 'TON', 'DOGE']);
});

// A calendar link is read access to the calendar: it goes to the server and
// never comes back, neither in the settings nor in the browser's storage.
test('a calendar link is saved write-only and can be disconnected', async ({ page }) => {
  // Port 1 refuses at once: the feed fails without reaching the internet.
  const link = 'webcal://127.0.0.1:1/published/2/e2e-secret-token';
  await page.goto('/');
  const board = page.locator('#calendar-board');
  await expect(board).toBeHidden();

  await page.locator('#settings-toggle').click();
  await page.locator('#set-calendars').fill(link);
  await page.locator('#settings button[type=submit]').click();
  await expect(page.locator('#set-status')).toHaveText('Сохранено');
  await expect(page.locator('#set-calendars')).toHaveValue('');
  await expect(page.locator('#set-calendars-hint')).toContainText('Подключено: 1');

  // The link is saved, so a failed load shows instead of an empty panel.
  await expect(board).toBeVisible();
  await expect(page.locator('#calendar')).toContainText('Календарь не загрузился');

  const settings = await (await page.request.get('/api/settings')).text();
  expect(settings).toContain('"calendars":1');
  expect(settings).not.toContain('e2e-secret-token');
  const kept = await page.evaluate(() => JSON.stringify({ ...localStorage }));
  expect(kept).not.toContain('e2e-secret-token');

  await page.locator('#set-calendars-off').click();
  await expect(page.locator('#set-status')).toHaveText('Календари отключены');
  await expect(board).toBeHidden();
  await expect(page.locator('#set-calendars-off')).toBeHidden();
});

test('a deleted task can be taken back, and is deleted once the toast is gone', async ({ page }) => {
  const title = `Удаляемая ${Date.now()}`;
  await page.request.post('/api/tasks', { data: { title } });
  await page.goto('/');
  const task = page.locator('#tasks li', { hasText: title });
  const onServer = async () => (await (await page.request.get('/api/tasks')).json())
    .some((t: { title: string }) => t.title === title);

  await task.hover();
  await task.getByRole('button', { name: `Удалить задачу: ${title}` }).click();
  await expect(task).toHaveCount(0);
  await expect(page.locator('#toast-text')).toContainText(title);
  await page.getByRole('button', { name: 'Вернуть' }).click();
  await expect(task).toBeVisible();
  expect(await onServer()).toBe(true);

  await task.hover();
  await task.getByRole('button', { name: `Удалить задачу: ${title}` }).click();
  await expect(task).toHaveCount(0);
  await expect.poll(onServer, { timeout: 10_000 }).toBe(false);
  await expect(page.locator('#toast')).not.toHaveAttribute('data-open');
});

test('the engine name lists the search prefixes', async ({ page }) => {
  await page.goto('/');
  const q = page.locator('#q');
  await q.fill('погода');
  await page.locator('#engine-hint').click();
  await page.locator('#prefixes button', { hasText: 'Яндекс' }).click();
  await expect(q).toHaveValue('y погода');
  await expect(q).toBeFocused();
  await expect(page.locator('#engine-hint')).toHaveText('Яндекс');
  await expect(page.locator('#prefixes')).toBeHidden();
});

test('bookmarks are put in order by dragging, or with Alt+arrows', async ({ page }) => {
  const tag = Date.now();
  const names = ['Альфа', 'Бета', 'Гамма'].map((n) => `${n} ${tag}`);
  for (const title of names) await page.request.post('/api/bookmarks', { data: { title, url: 'https://example.com' } });
  const order = async () => (await (await page.request.get('/api/bookmarks')).json())
    .map((b: { title: string }) => b.title).filter((t: string) => t.endsWith(String(tag)));
  const tile = (i: number) => page.locator('#bookmarks li', { hasText: names[i] });

  await page.goto('/');
  await page.locator('#edit-toggle').click();

  // Drag Альфа onto Гамма: it takes Гамма's place, the others make way.
  const from = await tile(0).boundingBox();
  const to = await tile(2).boundingBox();
  if (!from || !to) throw new Error('no tiles');
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, { steps: 12 });
  await page.mouse.up();
  await expect.poll(order).toEqual([names[1], names[2], names[0]]);
  await expect(page).toHaveURL(/\/$/); // the drag didn't open the link

  // From the keyboard: Alt+← moves it back one place.
  await tile(0).locator('a').focus();
  await page.keyboard.press('Alt+ArrowLeft');
  await expect.poll(order).toEqual([names[1], names[0], names[2]]);
  await expect(tile(0).locator('a')).toBeFocused();

  // And it stays so after a reload.
  await page.reload();
  const shown = await page.locator('#bookmarks li .label').allTextContents();
  expect(shown.filter((t) => t.endsWith(String(tag)))).toEqual([names[1], names[0], names[2]]);
});

test('the line under the date names the next event today and likely rain', async ({ page }) => {
  const now = Date.now();
  const iso = (ms: number) => new Date(ms).toISOString();
  const day = new Date(now);
  const from = `${day.getFullYear()}-${String(day.getMonth() + 1).padStart(2, '0')}-${String(day.getDate()).padStart(2, '0')}`;
  await page.route('**/api/calendar?*', (route) => route.fulfill({
    json: { configured: true, from, events: [
      { title: 'Прошедшее', start: iso(now - 3 * 3600e3), end: iso(now - 2 * 3600e3), allDay: false },
      { title: 'Созвон', start: iso(now + 20 * 60e3), end: iso(now + 50 * 60e3), allDay: false },
    ] },
  }));
  await page.goto('/');
  await expect(page.locator('#today')).toContainText(/Через (19|20) мин: Созвон/);
});

test('services fold into one line that says which are down', async ({ page }) => {
  const check = (ok: boolean) => ({ ok, statusCode: ok ? 200 : 503, latencyMs: 40, checkedAt: new Date().toISOString() });
  await page.route('**/api/services', (route) => route.fulfill({
    json: [
      { id: 1, name: 'Бот', url: 'https://a.example/health', lastCheck: check(true), history: [] },
      { id: 2, name: 'Парсер', url: 'https://b.example/health', lastCheck: check(false), history: [] },
    ],
  }));
  await page.goto('/');
  await page.evaluate(() => localStorage.removeItem('startpage.servicesOpen'));
  await page.reload();
  const summary = page.locator('#services-summary');
  await expect(summary).toHaveText(/Не отвечает: Парсер/);
  await expect(page.locator('#services')).toBeHidden();
  await summary.click();
  await expect(page.locator('#services')).toBeVisible();
  await expect(summary).toHaveAttribute('aria-expanded', 'true');
});
