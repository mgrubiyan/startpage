import { defineConfig, devices } from '@playwright/test';
import { baseURL, databaseURL, password, port, stateFile } from './env';

// Browser smoke tests of the page against a real server and Postgres. They
// check only the page's own behaviour: weather, rates, quotes and the painting
// come from third parties and may well fail in CI, which the page shows as such.
export default defineConfig({
  testDir: './tests',
  // One server and one database: tests run one after another.
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  // No retries: a flaky test should be fixed, not hidden.
  retries: 0,
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : 'list',
  globalSetup: './global-setup.ts',
  use: {
    baseURL,
    storageState: stateFile,
    locale: 'ru-RU',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'go run ../cmd/startpage',
    url: `${baseURL}/healthz`,
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: 'ignore',
    stderr: 'pipe',
    env: {
      DATABASE_URL: databaseURL,
      STARTPAGE_ADDR: `127.0.0.1:${port}`,
      STARTPAGE_PASSWORD: password,
      // The session tests log in several times; at 5 a minute a repeated run (--repeat-each) is refused.
      STARTPAGE_LOGIN_RATE: '60',
    },
  },
});
