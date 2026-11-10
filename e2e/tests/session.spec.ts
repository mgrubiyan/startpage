import { expect, test } from '@playwright/test';
import { password } from '../env';

// These log in and out themselves, so they start without the shared session.
test.use({ storageState: { cookies: [], origins: [] } });

test('the login screen refuses a wrong password and opens the page on the right one', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#login')).toBeVisible();
  await expect(page.locator('#app')).toBeHidden();

  await page.locator('#login-password').fill('not-the-password');
  await page.getByRole('button', { name: 'Войти' }).click();
  await expect(page.locator('#login-error')).toHaveText('Неверный пароль');

  await page.locator('#login-password').fill(password);
  await page.getByRole('button', { name: 'Войти' }).click();
  await expect(page.locator('#app')).toBeVisible();
  await expect(page.locator('#q')).toBeFocused();
});

test('logging out forgets the kept page', async ({ page }) => {
  const kept = () => page.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith('startpage.last.')));

  expect((await page.request.post('/api/login', { data: { password } })).ok()).toBe(true);
  await page.goto('/');
  await expect(page.locator('#app')).toBeVisible();
  await expect.poll(kept).toContain('startpage.last.settings');

  await page.getByRole('button', { name: 'Выйти' }).click();
  await expect(page.locator('#login')).toBeVisible();
  expect(await kept()).toEqual([]);

  // Nothing private flashes on the next open either.
  await page.reload();
  await expect(page.locator('#login')).toBeVisible();
  await expect(page.locator('#app')).toBeHidden();
});
