import { expect, test } from '@playwright/test';

test('a checked task stays where it is until the list is next loaded', async ({ page }) => {
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
  await page.request.post('/api/bookmarks', { data: { title: 'Пример', url: 'https://example.com' } });
  await page.goto('/');
  const tile = page.locator('#bookmarks li', { hasText: 'Пример' });
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
  await expect(page.locator('#tasks .title').first()).toBeVisible({ timeout: 1000 });
  release();
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
