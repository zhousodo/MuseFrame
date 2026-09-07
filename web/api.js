// MuseFrame API client — account sessions, JSON errors surfaced as {code,...}.
const KEY = 'mf.session';

// Packaged app (Capacitor WebView) → absolute production API base;
// browser served by the backend → same-origin relative URLs.
export const API_BASE = window.Capacitor ? (window.MF_CONFIG?.apiBase || '') : '';
export const apiUrl = (p) => (p?.startsWith('/') ? API_BASE + p : p);

export let token = localStorage.getItem(KEY) || null;
let imageToken = null;
let imageTokenExpiresAt = 0;
let imageTokenRequest = null;

export function setToken(t) {
  token = t;
  imageToken = null;
  imageTokenExpiresAt = 0;
  imageTokenRequest = null;
  localStorage.setItem(KEY, t);
}
export function clearToken() {
  token = null;
  imageToken = null;
  imageTokenExpiresAt = 0;
  imageTokenRequest = null;
  localStorage.removeItem(KEY);
}

/**
 * Validate a stored account session. Public catalogue requests do not need a
 * token, so a signed-out client must not silently mint an anonymous identity.
 * Returns 'account' for a valid registered account, 'none' when no account
 * session exists, or 'offline' when an existing token could not be checked.
 */
export async function ensureSession() {
  if (token) {
    try { await get('/v1/entitlements/me'); return 'account'; }
    catch (e) {
      if (e.code !== 'AUTH_REQUIRED') return 'offline';
      clearToken();
    }
  }
  return 'none';
}

async function request(method, path, body, extraHeaders = {}, raw = false) {
  const headers = { ...extraHeaders };
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body !== undefined && !raw) headers['Content-Type'] = 'application/json';
  const res = await fetch(apiUrl(path), { method, headers, body: raw ? body : body !== undefined ? JSON.stringify(body) : undefined });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data?.error?.message || `HTTP ${res.status}`);
    Object.assign(err, data?.error || {}, { status: res.status, details: data?.error?.details });
    throw err;
  }
  return data;
}

export const get = (p) => request('GET', p);
export const post = (p, body, headers) => request('POST', p, body, headers);
export const put = (p, body) => request('PUT', p, body, {}, true);
export const patch = (p, body) => request('PATCH', p, body);
export const del = (p) => request('DELETE', p);

/**
 * Fetch/cache an account-scoped one-hour image credential. Long-lived session
 * bearers must never appear in URLs (proxy logs, history and referrers retain
 * them); normal API calls continue to carry the bearer in a header.
 */
export async function ensureAssetToken() {
  if (!token) return null;
  if (imageToken && Date.now() < imageTokenExpiresAt - 60_000) return imageToken;
  if (!imageTokenRequest) {
    imageTokenRequest = get('/v1/assets/img-token').then((r) => {
      imageToken = r.token;
      const ttl = Math.min(Math.max(Number(r.ttlSeconds) || 3600, 60), 3600);
      imageTokenExpiresAt = Date.now() + ttl * 1000;
      return imageToken;
    }).finally(() => { imageTokenRequest = null; });
  }
  return imageTokenRequest;
}

export const assetUrl = (assetId) => imageToken
  ? apiUrl(`/v1/assets/${assetId}/file?img_token=${encodeURIComponent(imageToken)}`)
  : null;

export function track(name, props = {}) {
  post('/v1/events', { events: [{ name, props }] }).catch(() => {});
}
