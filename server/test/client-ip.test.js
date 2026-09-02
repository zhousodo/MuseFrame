// F02/F39 — the client address the rate limiter and free-grant IP cap key on.
// The old code was `cf-connecting-ip || x-forwarded-for[0] || socket`, all of
// which a caller can set: a fresh random value per request removed every
// per-IP limit, including the mail-bomb brake on /v1/auth/email/request.
import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import { makeClientIpResolver, bodyLimitFor, MAX_UPLOAD_BODY, MAX_JSON_BODY, MAX_ADMIN_BODY } from '../net.js';

const req = (peer, headers = {}) => ({ socket: { remoteAddress: peer }, headers });

describe('resolveClientIp — trusted proxy', () => {
  const resolve = makeClientIpResolver({ TRUSTED_PROXY: 'private' });

  test('an untrusted (public) peer cannot claim any address at all', () => {
    // The origin is reachable directly on plain HTTP, so this is the real
    // attack shape: headers set by the attacker, connecting from the internet.
    const r = req('203.0.113.9', {
      'cf-connecting-ip': '10.0.0.1',
      'x-forwarded-for': '10.0.0.2, 10.0.0.3',
      'x-real-ip': '10.0.0.4',
    });
    assert.equal(resolve(r), '203.0.113.9');
  });

  test('behind a trusted proxy, the RIGHTMOST xff entry wins (the appended one)', () => {
    // Caddy appends the real peer, so anything the client pre-seeded sits to
    // the left and is ignored. Taking parts[0] was the whole bug.
    assert.equal(resolve(req('127.0.0.1', { 'x-forwarded-for': '1.2.3.4, 198.51.100.7' })), '198.51.100.7');
  });

  test('a client-seeded xff cannot displace the proxy-appended value', () => {
    const spoofed = resolve(req('172.18.0.1', { 'x-forwarded-for': '9.9.9.9, 9.9.9.8, 198.51.100.7' }));
    assert.equal(spoofed, '198.51.100.7');
  });

  test('cf-connecting-ip is ignored unless explicitly trusted', () => {
    assert.equal(resolve(req('127.0.0.1', { 'cf-connecting-ip': '9.9.9.9', 'x-forwarded-for': '198.51.100.7' })), '198.51.100.7');
    const cfResolve = makeClientIpResolver({ TRUSTED_PROXY: 'private', TRUST_CF_CONNECTING_IP: 'true' });
    assert.equal(cfResolve(req('127.0.0.1', { 'cf-connecting-ip': '9.9.9.9', 'x-forwarded-for': '198.51.100.7' })), '9.9.9.9');
  });

  test('the docker bridge gateway counts as trusted (this is the real topology)', () => {
    assert.equal(resolve.isTrustedPeer('172.18.0.1'), true);
    assert.equal(resolve.isTrustedPeer('10.1.2.3'), true);
    assert.equal(resolve.isTrustedPeer('192.168.1.1'), true);
    assert.equal(resolve.isTrustedPeer('::1'), true);
    assert.equal(resolve.isTrustedPeer('172.32.0.1'), false); // outside RFC1918
    assert.equal(resolve.isTrustedPeer('203.0.113.9'), false);
  });

  test('IPv4-mapped IPv6 peers are normalised, not treated as strangers', () => {
    assert.equal(resolve(req('::ffff:127.0.0.1', { 'x-forwarded-for': '198.51.100.7' })), '198.51.100.7');
  });

  test('x-real-ip is a fallback only, never ahead of xff', () => {
    assert.equal(resolve(req('127.0.0.1', { 'x-real-ip': '198.51.100.7' })), '198.51.100.7');
  });

  test('no headers at all → the socket address', () => {
    assert.equal(resolve(req('127.0.0.1')), '127.0.0.1');
  });

  test("TRUSTED_PROXY=none never consults a header", () => {
    const r = makeClientIpResolver({ TRUSTED_PROXY: 'none' });
    assert.equal(r(req('127.0.0.1', { 'x-forwarded-for': '198.51.100.7' })), '127.0.0.1');
  });

  test('an explicit allow-list trusts only those peers', () => {
    const r = makeClientIpResolver({ TRUSTED_PROXY: '10.5.0.1' });
    assert.equal(r(req('10.5.0.1', { 'x-forwarded-for': '198.51.100.7' })), '198.51.100.7');
    assert.equal(r(req('10.5.0.2', { 'x-forwarded-for': '198.51.100.7' })), '10.5.0.2');
  });

  test('spoofing many distinct headers yields ONE bucket, not one per header', () => {
    // The property that actually matters: the rate limiter must see a single
    // key no matter how the attacker varies the headers.
    const seen = new Set();
    for (let i = 0; i < 500; i++) {
      seen.add(resolve(req('203.0.113.9', {
        'cf-connecting-ip': `10.0.${i >> 8}.${i & 255}`,
        'x-forwarded-for': `10.1.${i >> 8}.${i & 255}`,
      })));
    }
    assert.deepEqual([...seen], ['203.0.113.9']);
  });
});

describe('bodyLimitFor — per-route ceilings (F27)', () => {
  test('only the photo upload route gets the 26 MB ceiling', () => {
    assert.equal(bodyLimitFor('PUT', '/v1/assets/abc-123/upload'), MAX_UPLOAD_BODY);
  });
  test('ordinary JSON routes are capped at 64 KB', () => {
    for (const p of ['/v1/events', '/v1/auth/exchange', '/v1/generation-jobs', '/v1/purchases/verify']) {
      assert.equal(bodyLimitFor('POST', p), MAX_JSON_BODY, p);
    }
  });
  test('a POST to the upload path does not inherit the upload ceiling', () => {
    assert.equal(bodyLimitFor('POST', '/v1/assets/abc-123/upload'), MAX_JSON_BODY);
  });
  test('admin routes get their own middle ceiling', () => {
    assert.equal(bodyLimitFor('PUT', '/v1/admin/config'), MAX_ADMIN_BODY);
  });
});
