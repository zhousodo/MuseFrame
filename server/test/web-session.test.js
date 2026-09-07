// Browser-session regression coverage without a DOM: the API module must not
// create an anonymous account just because a public visitor opened the site.
import { test } from 'node:test';
import assert from 'node:assert/strict';

class MemoryStorage {
  constructor(entries = {}) { this.values = new Map(Object.entries(entries)); }
  getItem(key) { return this.values.get(key) ?? null; }
  setItem(key, value) { this.values.set(key, String(value)); }
  removeItem(key) { this.values.delete(key); }
}

async function loadClient(entries, responder) {
  globalThis.window = {};
  globalThis.localStorage = new MemoryStorage(entries);
  const requests = [];
  globalThis.fetch = async (url, init = {}) => {
    requests.push({ url: String(url), method: init.method || 'GET' });
    return responder(url, init);
  };
  const mod = await import(`../../web/api.js?session-test=${Math.random()}`);
  return { mod, requests };
}

test('a visitor with no token stays anonymous and makes no auth request', async () => {
  const { mod, requests } = await loadClient({}, () => { throw new Error('fetch must not run'); });
  assert.equal(await mod.ensureSession(), 'none');
  assert.equal(mod.token, null);
  assert.deepEqual(requests, []);
});

test('an old guest/expired token is cleared and never exchanged for another guest', async () => {
  const { mod, requests } = await loadClient({ 'mf.session': 'old-guest' }, async () => new Response(
    JSON.stringify({ error: { code: 'AUTH_REQUIRED', message: 'Sign in to continue.' } }),
    { status: 401, headers: { 'Content-Type': 'application/json' } },
  ));
  assert.equal(await mod.ensureSession(), 'none');
  assert.equal(mod.token, null);
  assert.equal(localStorage.getItem('mf.session'), null);
  assert.deepEqual(requests, [{ url: '/v1/entitlements/me', method: 'GET' }]);
});

test('a registered account token remains active after validation', async () => {
  const { mod, requests } = await loadClient({ 'mf.session': 'member-token' }, async () => new Response(
    JSON.stringify({ plan: 'free', availableUnits: 3 }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  ));
  assert.equal(await mod.ensureSession(), 'account');
  assert.equal(mod.token, 'member-token');
  assert.equal(localStorage.getItem('mf.session'), 'member-token');
  assert.deepEqual(requests, [{ url: '/v1/entitlements/me', method: 'GET' }]);
});
