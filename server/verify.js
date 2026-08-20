// Server-side identity & purchase verification (anti-tamper).
// Everything a client claims about who it is or what it bought is verified here
// against the issuer's cryptographic signature — never trusted on the client's
// word. Uses only Node built-ins (crypto + fetch); credentials come from env.
import crypto from 'node:crypto';
import { readFileSync } from 'node:fs';

// ---- JWKS-based ID token verification (Google Sign-In, Sign in with Apple) --

const jwksCache = new Map(); // url -> { keys, exp }
async function getJwks(url) {
  const c = jwksCache.get(url);
  if (c && c.exp > Date.now()) return c.keys;
  const res = await fetch(url, { signal: AbortSignal.timeout(10000) });
  const body = await res.json();
  jwksCache.set(url, { keys: body.keys, exp: Date.now() + 3600_000 });
  return body.keys;
}

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
  const keys = await getJwks(jwksUrl);
  const jwk = keys.find((k) => k.kid === header.kid && k.alg === (header.alg || 'RS256'));
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
  const audience = (process.env.GOOGLE_CLIENT_IDS || '').split(',').map((s) => s.trim()).filter(Boolean);
  if (!audience.length) { const e = new Error('Google Sign-In not configured'); e.code = 'PROVIDER_NOT_CONFIGURED'; throw e; }
  const c = await verifyIdToken(idToken, {
    jwksUrl: 'https://www.googleapis.com/oauth2/v3/certs',
    issuers: ['accounts.google.com', 'https://accounts.google.com'],
    audience,
  });
  return { subject: c.sub, email: c.email_verified ? c.email : null, name: c.name || null };
}

export async function verifyAppleIdToken(idToken) {
  const audience = (process.env.APPLE_BUNDLE_IDS || '').split(',').map((s) => s.trim()).filter(Boolean);
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
  saCache = JSON.parse(readFileSync(p, 'utf8'));
  return saCache;
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
  const res = await fetch('https://oauth2.googleapis.com/token', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ grant_type: 'urn:ietf:params:oauth:grant-type:jwt-bearer', assertion }),
    signal: AbortSignal.timeout(15000),
  });
  const body = await res.json();
  if (!body.access_token) throw new Error('play auth failed: ' + (body.error_description || res.status));
  playTokenCache = { token: body.access_token, exp: Date.now() + body.expires_in * 1000 };
  return body.access_token;
}

/**
 * Verify a Google Play purchase. `kind` is 'product' (one-time / consumable) or
 * 'subscription'. Returns { valid, expiresAt } or throws.
 */
export async function verifyPlayPurchase({ packageName, productId, purchaseToken, kind }) {
  const token = await getPlayAccessToken();
  const pkg = packageName || process.env.GOOGLE_PACKAGE_NAME;
  const base = `https://androidpublisher.googleapis.com/androidpublisher/v3/applications/${pkg}`;
  const url = kind === 'subscription'
    ? `${base}/purchases/subscriptions/${productId}/tokens/${purchaseToken}`
    : `${base}/purchases/products/${productId}/tokens/${purchaseToken}`;
  const res = await fetch(url, { headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(15000) });
  const body = await res.json();
  if (!res.ok) { const e = new Error('play verify failed: ' + (body.error?.message || res.status)); e.code = 'PURCHASE_INVALID'; throw e; }
  if (kind === 'subscription') {
    // 0 = payment pending is not entitled; require an active/expiry in the future.
    const expiry = Number(body.expiryTimeMillis || 0);
    return { valid: expiry > Date.now() && body.paymentState === 1, expiresAt: new Date(expiry).toISOString() };
  }
  // one-time: purchaseState 0 = purchased; 1 = cancelled
  return { valid: body.purchaseState === 0, expiresAt: null };
}

export const verifyConfig = {
  googleSignIn: !!(process.env.GOOGLE_CLIENT_IDS || '').trim(),
  appleSignIn: !!(process.env.APPLE_BUNDLE_IDS || '').trim(),
  playBilling: !!process.env.GOOGLE_SERVICE_ACCOUNT_JSON,
  allowMockPurchases: process.env.ALLOW_MOCK_PURCHASES === 'true',
  allowGuest: process.env.ALLOW_GUEST !== 'false',
  freeRequiresAuth: process.env.FREE_REQUIRES_AUTH === 'true',
};
