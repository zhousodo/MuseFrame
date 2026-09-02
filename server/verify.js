// Server-side identity & purchase verification (anti-tamper).
// Everything a client claims about who it is or what it bought is verified here
// against the issuer's cryptographic signature — never trusted on the client's
// word. Uses only Node built-ins (crypto + fetch); credentials come from env.
import crypto from 'node:crypto';
import { readFileSync } from 'node:fs';
import { cfg } from './configStore.js';

// ---- JWKS-based ID token verification (Google Sign-In, Sign in with Apple) --

const jwksCache = new Map(); // url -> { keys, exp }
/**
 * A failed fetch used to be cached exactly like a successful one: one 502 from
 * the issuer's edge stored `keys: undefined` for a full hour, and every sign-in
 * in that hour then died on `keys.find` — surfaced to the user as AUTH_INVALID,
 * i.e. "your account is wrong". Only a well-formed key set is cached; anything
 * else throws JWKS_UNAVAILABLE, which callers map to 503, not 401.
 */
export async function getJwks(url, { force = false } = {}) {
  const c = jwksCache.get(url);
  if (!force && c && c.exp > Date.now()) return c.keys;
  let res, body;
  try {
    res = await fetch(url, { signal: AbortSignal.timeout(10000) });
    body = await res.json();
  } catch (e) {
    const err = new Error('jwks fetch failed: ' + e.message);
    err.code = 'JWKS_UNAVAILABLE';
    throw err;
  }
  if (!res.ok || !Array.isArray(body?.keys) || !body.keys.length) {
    const err = new Error('jwks fetch failed: HTTP ' + res.status);
    err.code = 'JWKS_UNAVAILABLE';
    throw err;
  }
  jwksCache.set(url, { keys: body.keys, exp: Date.now() + 3600_000 });
  return body.keys;
}
/** Test hook: drop the memoised key sets. */
export function resetJwksCache() { jwksCache.clear(); }

const b64urlJson = (s) => JSON.parse(Buffer.from(s, 'base64url').toString('utf8'));

/**
 * Verify an OIDC ID token (RS256) against the issuer's JWKS.
 * Returns the validated claims, or throws. Checks signature, iss, aud, exp.
 */
async function verifyIdToken(idToken, { jwksUrl, issuers, audience }) {
  const parts = String(idToken).split('.');
  if (parts.length !== 3) throw new Error('malformed token');
  const header = b64urlJson(parts[0]);
  const claims = b64urlJson(parts[1]);
  const match = (ks) => ks.find((k) => k.kid === header.kid && k.alg === (header.alg || 'RS256'));
  let keys = await getJwks(jwksUrl);
  // A kid miss is normally a key rotation, not a forged token: refetch once,
  // bypassing the hour-long cache, before calling the token bad.
  let jwk = match(keys);
  if (!jwk) { keys = await getJwks(jwksUrl, { force: true }); jwk = match(keys); }
  if (!jwk) throw new Error('signing key not found');
  const pub = crypto.createPublicKey({ key: jwk, format: 'jwk' });
  const ok = crypto.verify('RSA-SHA256', Buffer.from(`${parts[0]}.${parts[1]}`), pub, Buffer.from(parts[2], 'base64url'));
  if (!ok) throw new Error('bad signature');
  if (!issuers.includes(claims.iss)) throw new Error('bad issuer');
  const auds = Array.isArray(audience) ? audience : [audience];
  if (!auds.includes(claims.aud)) throw new Error('bad audience');
  if (claims.exp * 1000 < Date.now()) throw new Error('token expired');
  return claims;
}

export async function verifyGoogleIdToken(idToken) {
  const audience = (cfg('google_client_ids') || '').split(',').map((s) => s.trim()).filter(Boolean);
  if (!audience.length) { const e = new Error('Google Sign-In not configured'); e.code = 'PROVIDER_NOT_CONFIGURED'; throw e; }
  const c = await verifyIdToken(idToken, {
    jwksUrl: 'https://www.googleapis.com/oauth2/v3/certs',
    issuers: ['accounts.google.com', 'https://accounts.google.com'],
    audience,
  });
  return { subject: c.sub, email: c.email_verified ? c.email : null, name: c.name || null };
}

export async function verifyAppleIdToken(idToken) {
  const audience = (cfg('apple_bundle_ids') || '').split(',').map((s) => s.trim()).filter(Boolean);
  if (!audience.length) { const e = new Error('Sign in with Apple not configured'); e.code = 'PROVIDER_NOT_CONFIGURED'; throw e; }
  const c = await verifyIdToken(idToken, {
    jwksUrl: 'https://appleid.apple.com/auth/keys',
    issuers: ['https://appleid.apple.com'],
    audience,
  });
  return { subject: c.sub, email: c.email || null, name: null };
}

// ---- Google Play purchase verification -------------------------------------
// Verifies a purchaseToken against the Play Developer API using a service
// account. The client cannot forge this — the token only validates if Google's
// own records confirm a real, still-valid purchase for our package.

let saCache = null;
function serviceAccount() {
  if (saCache) return saCache;
  const p = process.env.GOOGLE_SERVICE_ACCOUNT_JSON;
  if (!p) return null;
  try {
    saCache = JSON.parse(readFileSync(p, 'utf8'));
  } catch (e) {
    // A missing/unreadable/corrupt key file is an operator problem, not a bad
    // purchase. Raw ENOENT used to bubble out and get reported to the buyer as
    // 402 PURCHASE_INVALID — "your money is gone and your receipt is fake".
    const err = new Error('Play service account unreadable: ' + e.message);
    err.code = 'PROVIDER_NOT_CONFIGURED';
    throw err;
  }
  return saCache;
}

/**
 * Play purchase tokens and product ids are pasted straight into a Play API URL
 * path. Google's tokens are URL-safe base64 (`[A-Za-z0-9._-]`), so anything
 * outside that alphabet is either an attack or a client bug — and it mattered:
 * an unencoded `?`, `#`, `/` or `%xx` produced a URL that Google resolved to the
 * SAME purchase while our `purchases` table saw a brand-new
 * `external_transaction_id`, so one real purchase could be re-granted without
 * limit by appending `?x=1`, `#`, `%2E`, … to the token.
 */
export const PLAY_TOKEN_RE = /^[A-Za-z0-9._-]{1,512}$/;
export const PLAY_PRODUCT_RE = /^[A-Za-z0-9._-]{1,128}$/;
export function assertPlayToken(purchaseToken, productId) {
  if (typeof purchaseToken !== 'string' || !PLAY_TOKEN_RE.test(purchaseToken)) {
    const e = new Error('Malformed purchaseToken.');
    e.code = 'PURCHASE_TOKEN_INVALID';
    throw e;
  }
  if (typeof productId !== 'string' || !PLAY_PRODUCT_RE.test(productId)) {
    const e = new Error('Malformed store product id.');
    e.code = 'PURCHASE_TOKEN_INVALID';
    throw e;
  }
}

let playTokenCache = null;
async function getPlayAccessToken() {
  if (playTokenCache && playTokenCache.exp > Date.now() + 60_000) return playTokenCache.token;
  const sa = serviceAccount();
  if (!sa) { const e = new Error('Play verification not configured'); e.code = 'PROVIDER_NOT_CONFIGURED'; throw e; }
  const now = Math.floor(Date.now() / 1000);
  const header = Buffer.from(JSON.stringify({ alg: 'RS256', typ: 'JWT' })).toString('base64url');
  const claim = Buffer.from(JSON.stringify({
    iss: sa.client_email,
    scope: 'https://www.googleapis.com/auth/androidpublisher',
    aud: 'https://oauth2.googleapis.com/token',
    iat: now, exp: now + 3600,
  })).toString('base64url');
  const sig = crypto.sign('RSA-SHA256', Buffer.from(`${header}.${claim}`), sa.private_key).toString('base64url');
  const assertion = `${header}.${claim}.${sig}`;
  let res, body;
  try {
    res = await fetch('https://oauth2.googleapis.com/token', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ grant_type: 'urn:ietf:params:oauth:grant-type:jwt-bearer', assertion }),
      signal: AbortSignal.timeout(15000),
    });
    body = await res.json();
  } catch (e) {
    const err = new Error('play auth unreachable: ' + e.message);
    err.code = 'PROVIDER_UNAVAILABLE';
    throw err;
  }
  if (!body.access_token) {
    const err = new Error('play auth failed: ' + (body.error_description || res.status));
    err.code = 'PROVIDER_UNAVAILABLE';
    throw err;
  }
  playTokenCache = { token: body.access_token, exp: Date.now() + body.expires_in * 1000 };
  return body.access_token;
}

/**
 * Verify a Google Play purchase. `kind` is 'product' (one-time / consumable) or
 * 'subscription'. Returns { valid, expiresAt } or throws.
 */
export async function verifyPlayPurchase({ packageName, productId, purchaseToken, kind }) {
  assertPlayToken(purchaseToken, productId);
  const token = await getPlayAccessToken();
  const pkg = packageName || cfg('google_package_name');
  const base = `https://androidpublisher.googleapis.com/androidpublisher/v3/applications/${encodeURIComponent(pkg)}`;
  // encodeURIComponent on top of the alphabet check: belt and braces, and it
  // keeps the URL correct if Google ever widens the token alphabet.
  const path = kind === 'subscription'
    ? `${base}/purchases/subscriptions/${encodeURIComponent(productId)}/tokens/${encodeURIComponent(purchaseToken)}`
    : `${base}/purchases/products/${encodeURIComponent(productId)}/tokens/${encodeURIComponent(purchaseToken)}`;
  let res, body;
  try {
    res = await fetch(path, { headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(15000) });
    body = await res.json();
  } catch (e) {
    // Network / timeout / non-JSON: we could not ASK Google. That is not the
    // same answer as "Google says this purchase is not real", and must not
    // burn the receipt — see the pending-row handling in api.js.
    const err = new Error('play verify unreachable: ' + e.message);
    err.code = 'PROVIDER_UNAVAILABLE';
    throw err;
  }
  if (!res.ok) {
    // 5xx / 429 from Google is also "could not ask", not "invalid".
    const e = new Error('play verify failed: ' + (body?.error?.message || res.status));
    e.code = (res.status >= 500 || res.status === 429) ? 'PROVIDER_UNAVAILABLE' : 'PURCHASE_INVALID';
    throw e;
  }
  // orderId is Google's own identifier for the transaction and is what the
  // purchases table dedupes on: it is server-issued, so it cannot be varied by
  // the client, and it CHANGES on each subscription renewal (GPA.xxx..0, ..1).
  const shared = {
    orderId: typeof body.orderId === 'string' ? body.orderId : null,
    acknowledgementState: body.acknowledgementState ?? null,
    raw: body,
  };
  if (kind === 'subscription') {
    // paymentState: 0 pending · 1 received · 2 free trial · 3 pending deferred
    // upgrade/downgrade. Requiring exactly 1 rejected every free-trial and
    // every plan-change subscriber — people Google considers fully entitled.
    const expiry = Number(body.expiryTimeMillis || 0);
    const paid = [1, 2, 3].includes(Number(body.paymentState));
    return { ...shared, valid: expiry > Date.now() && paid, expiresAt: new Date(expiry).toISOString() };
  }
  // one-time: purchaseState 0 = purchased; 1 = cancelled; 2 = pending
  return { ...shared, valid: body.purchaseState === 0, expiresAt: null };
}

/**
 * Acknowledge a Play purchase. Google auto-refunds anything left unacknowledged
 * for three days, so a deployment that relies on the *server* to acknowledge
 * must turn this on. Off by default because the shipped client's billing plugin
 * may already acknowledge/consume on-device, and double-acknowledging is an
 * error there. Owner decision — see AUDIT-2026-09-02.md.
 */
export async function acknowledgePlayPurchase({ packageName, productId, purchaseToken, kind }) {
  if (process.env.PLAY_ACKNOWLEDGE !== 'true') return { skipped: true };
  assertPlayToken(purchaseToken, productId);
  const token = await getPlayAccessToken();
  const pkg = packageName || cfg('google_package_name');
  const base = `https://androidpublisher.googleapis.com/androidpublisher/v3/applications/${encodeURIComponent(pkg)}`;
  const path = kind === 'subscription'
    ? `${base}/purchases/subscriptions/${encodeURIComponent(productId)}/tokens/${encodeURIComponent(purchaseToken)}:acknowledge`
    : `${base}/purchases/products/${encodeURIComponent(productId)}/tokens/${encodeURIComponent(purchaseToken)}:acknowledge`;
  const res = await fetch(path, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: '{}',
    signal: AbortSignal.timeout(15000),
  });
  return { ok: res.ok, status: res.status };
}

// Live getters — every property reflects the current DB override / env var on
// each read, so admin changes take effect without a restart (except the two
// purely env-backed billing flags, which are unchanged by design).
export const verifyConfig = {
  get googleSignIn() { return !!(cfg('google_client_ids') || '').trim(); },
  get appleSignIn() { return !!(cfg('apple_bundle_ids') || '').trim(); },
  get playBilling() { return !!process.env.GOOGLE_SERVICE_ACCOUNT_JSON; },
  get allowMockPurchases() { return process.env.ALLOW_MOCK_PURCHASES === 'true'; },
  get allowGuest() { return !!cfg('allow_guest'); },
  get freeRequiresAuth() { return !!cfg('free_requires_auth'); },
};
