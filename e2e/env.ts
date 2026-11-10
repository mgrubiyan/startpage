// Shared by the config, the global setup and the tests.
export const port = 8095;
export const baseURL = `http://127.0.0.1:${port}`;

// A throwaway password for the throwaway instance the tests start.
export const password = 'e2e-password-not-a-secret';

// The session the global setup logs in with, reused by every test that
// doesn't test logging in or out itself: the server allows 5 logins a minute.
export const stateFile = '.auth/state.json';

export const databaseURL =
  process.env.E2E_DATABASE_URL ?? 'postgres://postgres:test@127.0.0.1:55432/startpage_e2e?sslmode=disable';
