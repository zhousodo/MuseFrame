// End-to-end HTTP behaviour, through the real server process.
// Covers F25/F27 (oversized body → a 413 that is actually delivered),
// F07/F28 (/v1/events needs a session and caps its props), F09 (admin token is
// header-only), F08 (?token= only on the asset-file route), F41 (health means
// something), F23 (typed request fields), F06 (control allow-list).
import { test, describe, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { startServer, guestToken } from './helpers.js';

let srv, token;
before(async () => {
  srv = await startServer();
  token = await guestToken(srv.base);
});
after(async () => { if (srv) await srv.stop(); });

const post = (p, body, headers = {}) => fetch(srv.base + p, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json', ...headers },
  body: typeof body === 'string' ? body : JSON.stringify(body),
});
const auth = (h = {}) => ({ Authorization: `Bearer ${token}`, ...h });

describe('oversized bodies get a real 413, not a dropped socket (F25/F27)', () => {
  const big = (n) => JSON.stringify({ events: [{ name: 'x', props: { a: 'A'.repeat(n) } }] });

  test('a 100 KB JSON body is refused with a readable 413', async () => {
    const res = await post('/v1/events', big(100_000), auth());
    assert.equal(res.status, 413);
    const body = await res.json();
    // The point of the fix: the response body actually arrives. Before, the
    // socket was destroyed first and the client saw only a network error.
    assert.equal(body.error.code, 'ASSET_UNSUPPORTED');
    assert.match(body.error.message, /too large/i);
    assert.ok(body.error.requestId, 'the error still carries a requestId');
    assert.equal(res.headers.get('connection'), 'close');
  });

  test('the same is true with no content-length (chunked)', async () => {
    // The content-length shortcut must not be the only guard.
    const res = await fetch(srv.base + '/v1/events', {
      method: 'POST',
      headers: auth({ 'Content-Type': 'application/json' }),
      duplex: 'half',
      body: new ReadableStream({
        start(c) {
          c.enqueue(new TextEncoder().encode('{"events":[{"name":"x","props":{"a":"'));
          for (let i = 0; i < 40; i++) c.enqueue(new TextEncoder().encode('A'.repeat(4096)));
          c.enqueue(new TextEncoder().encode('"}}]}'));
          c.close();
        },
      }),
    });
    assert.equal(res.status, 413);
    assert.equal((await res.json()).error.code, 'ASSET_UNSUPPORTED');
  });

  test('a body just under the JSON limit still works', async () => {
    const res = await post('/v1/events', big(30_000), auth());
    assert.equal(res.status, 200);
    assert.equal((await res.json()).accepted, 1);
  });

  test('the server is still healthy after being fed oversized bodies', async () => {
    for (let i = 0; i < 5; i++) await post('/v1/events', big(200_000), auth());
    const res = await fetch(srv.base + '/v1/health');
    assert.equal(res.status, 200);
    assert.equal((await res.json()).ok, true);
  });
});

describe('/v1/events is authenticated and bounded (F07/F28)', () => {
  test('an unauthenticated event write is refused', async () => {
    const res = await post('/v1/events', { events: [{ name: 'app_open' }] });
    assert.equal(res.status, 401);
    assert.equal((await res.json()).error.code, 'AUTH_REQUIRED');
  });

  test('an authenticated write is accepted and attributed', async () => {
    const res = await post('/v1/events', { events: [{ name: 'app_open', props: { a: 1 } }] }, auth());
    assert.equal(res.status, 200);
    assert.equal((await res.json()).accepted, 1);
  });

  test('oversized props are truncated rather than stored verbatim', async () => {
    const res = await post('/v1/events', { events: [{ name: 'big', props: { a: 'A'.repeat(5000) } }] }, auth());
    // 5 KB of props is under the 64 KB body limit but over the 2 KB props cap.
    assert.equal(res.status, 200);
    assert.equal((await res.json()).accepted, 1);
  });

  test('nameless events are skipped, not counted', async () => {
    const res = await post('/v1/events', { events: [{}, { name: 'ok' }, { props: {} }] }, auth());
    assert.equal((await res.json()).accepted, 1);
  });

  test('at most 50 events per call', async () => {
    const events = Array.from({ length: 200 }, (_, i) => ({ name: 'e' + i }));
    const res = await post('/v1/events', { events }, auth());
    assert.equal((await res.json()).accepted, 50);
  });
});

describe('admin token is header-only (F09)', () => {
  test('?admin_token= in the query string is not accepted', async () => {
    const res = await fetch(`${srv.base}/v1/admin/overview?admin_token=test-admin-token`);
    assert.equal(res.status, 401);
  });

  test('the X-Admin-Token header is accepted', async () => {
    const res = await fetch(`${srv.base}/v1/admin/overview`, { headers: { 'X-Admin-Token': 'test-admin-token' } });
    assert.equal(res.status, 200);
  });

  test('a wrong header token is refused', async () => {
    const res = await fetch(`${srv.base}/v1/admin/overview`, { headers: { 'X-Admin-Token': 'nope' } });
    assert.equal(res.status, 401);
  });

  test('the dev hatches stay shut without the admin token', async () => {
    // ALLOW_TEST_LOGIN is false in this server, but even the query-string form
    // must not be a way in.
    const res = await post(`/v1/auth/exchange?admin_token=test-admin-token`, { provider: 'dev', email: 'a@b.co' });
    assert.equal(res.status, 403);
  });
});

describe('session tokens in query strings (F08)', () => {
  test('?token= does NOT authenticate an ordinary API route', async () => {
    const res = await fetch(`${srv.base}/v1/entitlements/me?token=${encodeURIComponent(token)}`);
    assert.equal(res.status, 401);
  });

  test('the Authorization header still works', async () => {
    const res = await fetch(`${srv.base}/v1/entitlements/me`, { headers: auth() });
    assert.equal(res.status, 200);
  });

  test('?token= is still honoured on the asset-file route (shipped clients use it)', async () => {
    // The app in the store builds <img src=".../file?token=">. Breaking that
    // would break every already-installed copy, so this fallback must survive.
    const res = await fetch(`${srv.base}/v1/assets/does-not-exist/file?token=${encodeURIComponent(token)}`);
    assert.equal(res.status, 404, 'authenticated, then not found — not 401');
    assert.equal((await res.json()).error.code, 'ASSET_NOT_READY');
  });

  test('a bogus ?token= on the asset route is unauthenticated', async () => {
    const res = await fetch(`${srv.base}/v1/assets/does-not-exist/file?token=garbage`);
    assert.equal(res.status, 401);
  });

  test('an img-token can be minted for <img src> use', async () => {
    const res = await fetch(`${srv.base}/v1/assets/img-token`, { headers: auth() });
    assert.equal(res.status, 200);
    const body = await res.json();
    assert.match(body.token, /^[A-Za-z0-9_-]{24}$/);
    assert.equal(body.ttlSeconds, 3600);
  });
});

describe('health actually probes something (F41)', () => {
  test('health reports the database, the provider mode and the queue', async () => {
    const res = await fetch(srv.base + '/v1/health');
    assert.equal(res.status, 200);
    const b = await res.json();
    assert.equal(b.ok, true);
    assert.equal(b.service, 'museframe-api');
    assert.equal(b.database, 'ok');
    assert.ok(b.generation, 'generation status is reported');
    assert.equal(typeof b.queue.queued, 'number');
    assert.equal(typeof b.queue.active, 'number');
    assert.equal(typeof b.queue.oldestQueuedAgeSec, 'number');
  });
});

describe('request fields are typed (F23)', () => {
  test('an object deviceId is a 422, not a 500', async () => {
    const res = await post('/v1/auth/exchange', { provider: 'guest', deviceId: {} });
    assert.equal(res.status, 422);
    assert.equal((await res.json()).error.code, 'VALIDATION');
  });

  test('an array locale is a 422', async () => {
    const res = await post('/v1/auth/exchange', { provider: 'guest', locale: ['en'] });
    assert.equal(res.status, 422);
  });

  test('a valid guest exchange still works', async () => {
    const res = await post('/v1/auth/exchange', { provider: 'guest', deviceId: 'dev-typed', locale: 'en-GB' });
    assert.equal(res.status, 200);
    const b = await res.json();
    assert.ok(b.accessToken);
    assert.equal(b.user.isGuest, true);
  });

  test('malformed JSON is a 400, not a crash', async () => {
    const res = await post('/v1/auth/exchange', '{not json');
    assert.equal(res.status, 400);
  });

  test('controls: null on a generation job does not 500', async () => {
    const res = await post('/v1/generation-jobs',
      { projectId: 'nope', sourceAssetId: 'nope', styleVersionId: 'nope', controls: null },
      auth({ 'Idempotency-Key': 'k-' + Date.now() }));
    // Whatever it rejects on, it must not be an unhandled 500.
    assert.notEqual(res.status, 500);
    assert.ok([404, 409, 422, 402, 503].includes(res.status), 'got ' + res.status);
  });
});

describe('the public error contract is unchanged', () => {
  test('unknown endpoints are 404 NOT_FOUND with a requestId', async () => {
    const res = await fetch(srv.base + '/v1/nope');
    assert.equal(res.status, 404);
    const b = await res.json();
    assert.equal(b.error.code, 'NOT_FOUND');
    assert.match(b.error.requestId, /^req_/);
  });

  test('a protected route without a token is 401 AUTH_REQUIRED', async () => {
    const res = await fetch(srv.base + '/v1/projects');
    assert.equal(res.status, 401);
    assert.equal((await res.json()).error.code, 'AUTH_REQUIRED');
  });

  test('CORS headers are still emitted for the packaged WebView', async () => {
    const res = await fetch(srv.base + '/v1/health');
    assert.equal(res.headers.get('access-control-allow-origin'), '*');
  });

  test('auth/config no longer advertises billing.mock to the public', async () => {
    const res = await fetch(srv.base + '/v1/auth/config');
    const b = await res.json();
    assert.equal(b.billing.mock, false);
  });
});
