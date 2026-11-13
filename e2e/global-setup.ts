import { request } from '@playwright/test';
import { baseURL, password, stateFile } from './env';

// Logs in once for the tests and empties what they fill in: a local database
// outlives the run, and the tests assert on whole lists.
export default async function globalSetup() {
  const api = await request.newContext({ baseURL });
  const login = await api.post('/api/login', { data: { password } });
  if (!login.ok()) throw new Error(`login: HTTP ${login.status()}`);

  for (const t of await (await api.get('/api/tasks')).json()) {
    await api.delete(`/api/tasks/${t.id}`);
  }
  for (const b of await (await api.get('/api/bookmarks')).json()) {
    await api.delete(`/api/bookmarks/${b.id}`);
  }
  await api.put('/api/note', { data: { text: '' } });

  await api.storageState({ path: stateFile });
  await api.dispose();
}
