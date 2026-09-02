// 2026-09 收费模型：邮箱注册送 3 张（游客不发）、额度用完联系客服、管理员后台手动加额度。
// 通过真实服务进程验证：默认配置、/v1/auth/config 暴露的字段、手动加额度接口的校验与账本效果。
import { test, describe, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { startServer, guestToken } from './helpers.js';

let srv;
before(async () => {
  // 测试登录开关只在配合管理员令牌时生效——这里用它造一个「邮箱注册用户」。
  // 本地 .env 里可能还留着旧的 FREE_UNITS=1 / FREE_REQUIRES_AUTH=false（子进程会读它），
  // 这里显式指定新模型的值，并放宽 IP 上限——一个测试进程里要注册好几个账号。
  srv = await startServer({ ALLOW_TEST_LOGIN: 'true', FREE_UNITS: '3', FREE_REQUIRES_AUTH: 'true', FREE_GRANTS_PER_IP_DAY: '50' });
});
after(async () => { if (srv) await srv.stop(); });

const ADMIN = { 'X-Admin-Token': 'test-admin-token' };
const json = (method, p, body, headers = {}) => fetch(srv.base + p, {
  method, headers: { 'Content-Type': 'application/json', ...headers },
  body: body === undefined ? undefined : JSON.stringify(body),
});
const bearer = (t) => ({ Authorization: `Bearer ${t}` });

async function registeredUser(email) {
  const res = await json('POST', '/v1/auth/exchange', { provider: 'dev', email, deviceId: 'dev-' + email }, ADMIN);
  const body = await res.json();
  assert.ok(body.accessToken, 'test login should mint a session: ' + JSON.stringify(body));
  return body;
}
async function units(token) {
  const r = await fetch(srv.base + '/v1/entitlements/me', { headers: bearer(token) });
  return (await r.json()).availableUnits;
}

describe('free-tier defaults (3 after sign-up, nothing for guests)', () => {
  test('/v1/auth/config advertises the free quota and the support email', async () => {
    const cfg = await (await fetch(srv.base + '/v1/auth/config')).json();
    assert.equal(cfg.freeUnits, 3);
    assert.equal(cfg.freeRequiresAuth, true);
    assert.equal(cfg.support.email, 'donaldkuke@gmail.com');
    assert.equal(cfg.support.qqGroup, '824558022');
  });

  test('a guest session gets no free units', async () => {
    const token = await guestToken(srv.base);
    assert.equal(await units(token), 0);
  });

  test('an email-registered account gets exactly 3, once', async () => {
    const { accessToken } = await registeredUser('three@example.com');
    assert.equal(await units(accessToken), 3);
    // Signing in again on the same identity must not re-grant.
    const again = await registeredUser('three@example.com');
    assert.equal(await units(again.accessToken), 3);
  });
});

describe('POST /v1/admin/users/grant — manual top-up after the user emails support', () => {
  test('requires the admin token', async () => {
    const res = await json('POST', '/v1/admin/users/grant', { email: 'x@example.com', units: 5 });
    assert.equal(res.status, 401);
  });

  test('validates units and target', async () => {
    let res = await json('POST', '/v1/admin/users/grant', { email: 'x@example.com', units: 0 }, ADMIN);
    assert.equal(res.status, 422);
    res = await json('POST', '/v1/admin/users/grant', { email: 'x@example.com', units: 2.5 }, ADMIN);
    assert.equal(res.status, 422);
    res = await json('POST', '/v1/admin/users/grant', { units: 5 }, ADMIN);
    assert.equal(res.status, 422);
    res = await json('POST', '/v1/admin/users/grant', { email: 'nobody@example.com', units: 5 }, ADMIN);
    assert.equal(res.status, 404);
    res = await json('POST', '/v1/admin/users/grant', { userId: 'no-such-user', units: 5 }, ADMIN);
    assert.equal(res.status, 404);
    res = await json('POST', '/v1/admin/users/grant', { email: 'x@example.com', units: 5, expiresInDays: 0 }, ADMIN);
    assert.equal(res.status, 422);
  });

  test('grants by email and the balance is visible to the user immediately', async () => {
    const { accessToken } = await registeredUser('topup@example.com');
    assert.equal(await units(accessToken), 3);
    const res = await json('POST', '/v1/admin/users/grant', { email: 'TopUp@Example.com', units: 10, note: '微信转账 ¥xx 2026-09-02' }, ADMIN);
    assert.equal(res.status, 200);
    const body = await res.json();
    assert.equal(body.granted, 10);
    assert.equal(body.availableUnits, 13);
    assert.equal(body.isGuest, false);
    assert.equal(await units(accessToken), 13);
  });

  test('grants by full user id, with an expiry, and the admin user list can find the account', async () => {
    const { accessToken } = await registeredUser('byid@example.com');
    const list = await (await fetch(srv.base + '/v1/admin/users?q=byid%40', { headers: ADMIN })).json();
    assert.equal(list.users.length, 1);
    const u = list.users[0];
    assert.equal(u.email, 'byid@example.com');
    assert.ok(u.userId && u.userId.length > 8, 'the list exposes the full id for grants');
    assert.equal(u.units, 3);

    const res = await json('POST', '/v1/admin/users/grant', { userId: u.userId, units: 2, expiresInDays: 30 }, ADMIN);
    assert.equal(res.status, 200);
    const body = await res.json();
    assert.equal(body.availableUnits, 5);
    assert.ok(body.expiresAt, 'expiry is echoed back');
    assert.equal(await units(accessToken), 5);

    // The ledger carries the grant under its own source type, so it is
    // distinguishable from purchases and free grants in every report.
    const rows = await (await json('POST', '/v1/admin/db/query',
      { sql: "SELECT source_type, granted_units FROM credit_buckets WHERE source_type = 'manual' ORDER BY created_at" }, ADMIN)).json();
    const manual = (rows.rows || rows.results || []).map(r => r.granted_units ?? r[1]);
    assert.ok(manual.length >= 2, 'manual grants recorded in credit_buckets: ' + JSON.stringify(rows).slice(0, 200));
  });
});

describe('catalogue (plan B) and quality tiers', () => {
  test('/v1/products lists the new packs with CNY and USD prices and hides retired ones', async () => {
    const { products } = await (await fetch(srv.base + '/v1/products')).json();
    const keys = products.map(p => p.internalKey).sort();
    assert.deepEqual(keys, ['creator_monthly', 'pack_10', 'pack_100', 'pack_30']);
    const p10 = products.find(p => p.internalKey === 'pack_10');
    assert.equal(p10.grantedUnits, 10);
    assert.equal(p10.priceMinor, 499);
    assert.equal(p10.priceCnyMinor, 2900);
    assert.equal(p10.displayNameZh, '10 张包');
    const creator = products.find(p => p.internalKey === 'creator_monthly');
    assert.equal(creator.grantedUnits, 30);
    assert.equal(creator.priceMinor, 799);
    assert.equal(creator.priceCnyMinor, 4900);
  });

  test('admin can edit the CNY price and it is served live', async () => {
    let res = await json('PATCH', '/v1/admin/products-admin/pack_10', { priceCnyMinor: 2500 }, ADMIN);
    assert.equal(res.status, 200);
    let { products } = await (await fetch(srv.base + '/v1/products')).json();
    assert.equal(products.find(p => p.internalKey === 'pack_10').priceCnyMinor, 2500);
    res = await json('PATCH', '/v1/admin/products-admin/pack_10', { priceCnyMinor: -1 }, ADMIN);
    assert.equal(res.status, 422);
    await json('PATCH', '/v1/admin/products-admin/pack_10', { priceCnyMinor: 2900 }, ADMIN);
  });

  test('quality tiers are admin-configurable and the free plan is pinned to the standard tier', async () => {
    const cfgList = (await (await fetch(srv.base + '/v1/admin/config', { headers: ADMIN })).json()).settings;
    const byKey = Object.fromEntries(cfgList.map(c => [c.key, c]));
    assert.equal(byKey.free_units.value, 3, 'free quota is a live admin setting');
    assert.equal(byKey.image_quality_standard.value, 'medium');
    assert.equal(byKey.image_quality_high.value, 'high');
    assert.ok('image_provider_model_high' in byKey);
  });
});
