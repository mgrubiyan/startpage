import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';

// Accessibility, checked by axe-core against WCAG 2.2 A and AA: names for
// controls, contrast, roles, labels. Each violation is listed by rule and the
// elements it was found on, so a failure says what to fix.
// The third-party data is fixed, so every run checks the same page: quotes
// that rise and fall (their colours are what fails contrast first), rates with
// both arrows, and no painting, which would swap the whole palette whenever the
// museum answers in time.
async function steady(page: Page) {
  const board = (source: string, currency: string, quotes: [string, number][]) => ({
    source, delayMinutes: 0, stale: false,
    quotes: quotes.map(([symbol, change]) => ({ symbol, name: symbol, price: 100, change, currency })),
  });
  await page.route('**/api/markets', (route) => route.fulfill({ json: {
    available: true,
    crypto: board('Binance', 'USDT', [['BTC', 2.5], ['ETH', -1.25], ['SOL', 0]]),
    stocks: board('MOEX', 'RUB', [['SBER', 0.75], ['GAZP', -3]]),
  } }));
  await page.route('**/api/rates', (route) => route.fulfill({ json: {
    date: new Date().toISOString(), stale: false,
    usd: { value: 81.2, previous: 80.9 }, eur: { value: 94.1, previous: 94.6 },
  } }));
  await page.route('**/api/art', (route) => route.fulfill({ status: 503, json: { error: 'off in this test' } }));
}

async function violations(page: Page) {
  const { violations } = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze();
  return violations.map((v) => `${v.id}: ${v.help} → ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
}

test('the page passes axe', async ({ page }) => {
  await steady(page);
  await page.goto('/');
  await expect(page.locator('#app')).toBeVisible();
  await expect(page.locator('#tasks')).toBeVisible();
  await expect(page.locator('#markets .chg.pos').first()).toBeVisible();
  await expect(page.locator('#rates-value .down')).toBeVisible();
  expect(await violations(page)).toEqual([]);
});

for (const colorScheme of ['light', 'dark'] as const) {
  test(`the open settings, picker and edit modes pass axe (${colorScheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme });
    await steady(page);
    // Tiles side by side, so their × buttons are checked for size and spacing.
    for (const title of ['Один', 'Два', 'Три', 'Четыре', 'Пять']) {
      await page.request.post('/api/bookmarks', { data: { title: `${title} ${Date.now()}`, url: 'https://example.com' } });
    }
    await page.goto('/');
    await page.locator('#settings-toggle').click();
    await page.locator('#edit-toggle').click();
    await page.locator('#markets-edit').click();
    await expect(page.locator('#settings')).toBeVisible();
    expect(await violations(page)).toEqual([]);
  });
}

test.describe('signed out', () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test('the login screen passes axe', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('#login')).toBeVisible();
    expect(await violations(page)).toEqual([]);
  });
});
