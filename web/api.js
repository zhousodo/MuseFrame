// MuseFrame API client — guest-first session, JSON errors surfaced as {code,...}.
const KEY = 'mf.session';

// Packaged app (Capacitor WebView) → absolute production API base;
// browser served by the backend → same-origin relative URLs.
export const API_BASE = window.Capacitor ? (window.MF_CONFIG?.apiBase || '') : '';
export const apiUrl = (p) => (p?.startsWith('/') ? API_BASE + p : p);

export let token = localStorage.getItem(KEY) || null;

export function setToken(t) {
  token = t;
  localStorage.setItem(KEY, t);
}

export async function ensureSession(deviceId) {
  if (token) {
    try { await get('/v1/entitlements/me'); return; }
    catch (e) { if (e.code !== 'AUTH_REQUIRED') return; token = null; }
  }
  const res = await fetch(apiUrl('/v1/auth/exchange'), {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ provider: 'guest', locale: navigator.language, deviceId }),
  }).then(r => r.json());
  if (res.accessToken) setToken(res.accessToken);
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

export const assetUrl = (assetId) => apiUrl(`/v1/assets/${assetId}/file?token=${encodeURIComponent(token)}`);

export function track(name, props = {}) {
  post('/v1/events', { events: [{ name, props }] }).catch(() => {});
}
