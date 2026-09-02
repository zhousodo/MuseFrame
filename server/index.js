// MuseFrame server — API + static web app. Zero-framework Node http.
import './env.js'; // must stay first — populates process.env before adapters load
import http from 'node:http';
import { readFileSync, existsSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { db, q1, run, uuid, now, pruneOldRows } from './db.js';
import { seedCatalog, seedProducts } from './styles.js';
import { routes, authenticate, ApiError } from './api.js';
import { recoverJobs, drainWorker } from './jobs.js';
import { resolveClientIp, bodyLimitFor } from './net.js';

const ROOT = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const WEB = path.join(ROOT, 'web');
const PORT = process.env.PORT || 8787;

seedCatalog({ db, uuid, now, q1, run });
seedProducts({ q1, run, uuid });
recoverJobs();
// Retention: nothing else ever deleted a telemetry row or an expired session.
try { console.log('[boot] pruned', JSON.stringify(pruneOldRows())); } catch (e) { console.error('[boot] prune failed:', e.message); }
setInterval(() => {
  try { pruneOldRows(); } catch (e) { console.error('[prune]', e.message); }
}, 24 * 3600_000).unref?.();

const MIME = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8', '.png': 'image/png', '.jpg': 'image/jpeg',
  '.svg': 'image/svg+xml', '.json': 'application/json', '.woff2': 'font/woff2',
};

/**
 * Read a request body, refusing anything over `limit`.
 *
 * Two things went wrong before. The limit was a flat 26 MB for every route, so
 * any unauthenticated POST could pin 26 MB of heap in a 512 MB container. And
 * on refusal the socket was destroyed immediately — which meant the 413 was
 * never delivered: the client saw a bare network error and the app retried the
 * same oversized upload.
 *
 * So: stop BUFFERING at the limit (the memory is the thing that hurts), but
 * keep reading and discarding for a bounded budget so the request finishes
 * normally and the 413 can be written on a healthy connection. A caller that
 * blows through the drain budget too is cut off — at that point it is a flood,
 * not a client with a big photo.
 */
function readBody(req, limit) {
  return new Promise((resolve, reject) => {
    const drainBudget = Math.min(limit * 4, 8 * 1024 * 1024);
    const tooLarge = () => new ApiError(413, 'ASSET_UNSUPPORTED', 'Payload too large.');
    let chunks = [];
    let size = 0, drained = 0, over = false, settled = false;
    let timer = null;

    const settle = (fn, arg) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      chunks = null;
      fn(arg);
    };

    const startDrain = () => {
      over = true;
      chunks = null; // release what we already buffered
      // Never let a slow-loris flood hold the connection open indefinitely.
      timer = setTimeout(() => { settle(reject, tooLarge()); req.destroy(); }, 5_000);
      timer.unref?.();
    };

    // Cheapest rejection: the sender already told us how big it is. Still
    // drained rather than cut, so the response is readable.
    const declared = Number(req.headers['content-length']);
    if (Number.isFinite(declared) && declared > limit) startDrain();

    req.on('data', (c) => {
      if (settled) return;
      if (over) {
        drained += c.length;
        if (drained > drainBudget) { settle(reject, tooLarge()); req.destroy(); }
        return;
      }
      size += c.length;
      if (size > limit) { startDrain(); return; }
      chunks.push(c);
    });
    req.on('end', () => {
      if (over) return settle(reject, tooLarge());
      const buf = Buffer.concat(chunks || []);
      settle(resolve, buf);
    });
    req.on('error', (e) => settle(reject, over ? tooLarge() : e));
    req.on('aborted', () => settle(reject, over ? tooLarge() : new ApiError(400, 'VALIDATION', 'Request aborted.')));
  });
}

// Lightweight sliding-window rate limiter keyed by client IP + bucket. Blunts
// automated abuse of account creation, generation and purchase endpoints.
const rlHits = new Map();
// Bounded: the key is derived from the client address, so an unbounded map was
// itself a memory-growth lever. Oldest-inserted entries go first (Map preserves
// insertion order), which is close enough to LRU for a sliding window.
const RL_MAX_KEYS = Number(process.env.RATE_LIMIT_MAX_KEYS) || 50_000;
setInterval(() => { const now = Date.now(); for (const [k, v] of rlHits) if (v.reset < now) rlHits.delete(k); }, 60_000).unref?.();
function rateLimit(ip, bucket, limit, windowMs) {
  const key = `${bucket}:${ip}`;
  const now = Date.now();
  let e = rlHits.get(key);
  if (!e || e.reset < now) {
    if (rlHits.size >= RL_MAX_KEYS) {
      for (const k of rlHits.keys()) { rlHits.delete(k); if (rlHits.size < RL_MAX_KEYS * 0.9) break; }
    }
    e = { count: 0, reset: now + windowMs }; rlHits.set(key, e);
  }
  e.count++;
  return e.count <= limit ? 0 : Math.ceil((e.reset - now) / 1000);
}
const RL_RULES = [
  { re: /^\/v1\/auth\/exchange$/, limit: 20, windowMs: 600_000 },      // 20 sign-ins / 10 min
  { re: /^\/v1\/auth\/email\/request$/, limit: 5, windowMs: 600_000 }, // 5 codes / 10 min (email cost + abuse)
  { re: /^\/v1\/auth\/email\/verify$/, limit: 20, windowMs: 600_000 },
  { re: /^\/v1\/generation-jobs$/, m: 'POST', limit: 40, windowMs: 600_000 },
  { re: /^\/v1\/purchases\/verify$/, limit: 30, windowMs: 600_000 },
  { re: /^\/v1\/assets\/upload-intents$/, limit: 60, windowMs: 600_000 },
  // Telemetry: fire-and-forget on the client, so a generous ceiling still turns
  // an ingest flood into 429s instead of unbounded rows in `events`.
  { re: /^\/v1\/events$/, m: 'POST', limit: 120, windowMs: 600_000 },
  // Admin surface: the token is header-only and compared in constant time, but
  // without a ceiling it could still be guessed online — and a guessed token now
  // hands out credits. 120/min is far above any human operator's click rate.
  { re: /^\/v1\/admin\//, limit: 120, windowMs: 60_000 },
];

const server = http.createServer(async (req, res) => {
  const requestId = `req_${uuid().slice(0, 8)}`;
  const url = new URL(req.url, `http://${req.headers.host}`);
  const clientIp = resolveClientIp(req);
  // CORS: the packaged mobile app calls from a WebView origin.
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Methods', 'GET, POST, PUT, PATCH, DELETE, OPTIONS');
  res.setHeader('Access-Control-Allow-Headers', 'Content-Type, Authorization, Idempotency-Key');
  if (req.method === 'OPTIONS') { res.writeHead(204).end(); return; }
  try {
    if (url.pathname.startsWith('/v1/')) {
      for (const rule of RL_RULES) {
        if ((rule.m && rule.m !== req.method) || !rule.re.test(url.pathname)) continue;
        const retry = rateLimit(clientIp, rule.re.source, rule.limit, rule.windowMs);
        if (retry) { res.writeHead(429, { 'Content-Type': 'application/json', 'Retry-After': retry }); res.end(JSON.stringify({ error: { code: 'RATE_LIMITED', message: 'Too many requests.', requestId, details: { retryAfterSeconds: retry } } })); return; }
      }
      for (const r of routes) {
        if (r.method !== req.method) continue;
        const m = url.pathname.match(r.pattern);
        if (!m) continue;
        const user = authenticate(req, url);
        const raw = ['POST', 'PUT', 'PATCH'].includes(req.method)
          ? await readBody(req, bodyLimitFor(req.method, url.pathname)) : null;
        let body = null;
        if (raw && (req.headers['content-type'] || '').includes('application/json')) {
          try { body = JSON.parse(raw.toString('utf8')); }
          catch { throw new ApiError(400, 'VALIDATION', 'Invalid JSON body.'); }
        }
        const result = r.handler({ req, res, url, user, params: m.slice(1), body, raw, clientIp });
        const value = result instanceof Promise ? await result : result;
        if (value === null && res.writableEnded) return; // handler streamed its own response
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(value));
        return;
      }
      throw new ApiError(404, 'NOT_FOUND', 'No such endpoint.');
    }

    // Static web app
    let p = url.pathname === '/' ? '/index.html' : url.pathname;
    p = path.normalize(p).replace(/^([.][.][/\\])+/, '');
    const file = path.join(WEB, p);
    if (file.startsWith(WEB + path.sep) && existsSync(file) && statSync(file).isFile()) {
      const ext = path.extname(file);
      // HTML/JS/CSS must revalidate every load — a cached app.css without the
      // html.web rules is exactly how the desktop site came up in the phone frame.
      const cache = ['.html', '.js', '.css'].includes(ext) ? 'no-cache' : 'public, max-age=86400';
      res.writeHead(200, { 'Content-Type': MIME[ext] || 'application/octet-stream', 'Cache-Control': cache });
      res.end(readFileSync(file));
      return;
    }
    // SPA fallback
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-cache' });
    res.end(readFileSync(path.join(WEB, 'index.html')));
  } catch (e) {
    const status = e instanceof ApiError ? e.status : 500;
    const code = e instanceof ApiError ? e.code : 'INTERNAL_ERROR';
    if (status >= 500) console.error(`[${requestId}]`, e);
    if (!res.writableEnded) {
      const headers = { 'Content-Type': 'application/json' };
      // The rest of an oversized body is still in flight and we stopped reading
      // it. Announce the close so the client does not try to reuse the
      // connection, write the error, then drop the socket once it is out.
      // The oversized request was drained rather than cut, so the connection is
      // healthy and the client can read this. Close anyway: there is no point
      // keeping a connection alive for a sender that just overran its limit.
      if (status === 413) headers.Connection = 'close';
      res.writeHead(status, headers);
      res.end(JSON.stringify({ error: { code, message: e.message, requestId, details: e.details } }));
    }
  }
});

// Dev escape hatches are gated on the admin token as well as their flag, but a
// flag left on in production is still a mistake worth shouting about at boot.
for (const [flag, what] of [['ALLOW_MOCK_PURCHASES', '演示购买'], ['ALLOW_TEST_LOGIN', '测试登录']]) {
  if (process.env[flag] === 'true') console.warn(`[boot] ⚠️ ${flag}=true（${what}）——仅限开发；生产请设为 false。当前需管理员令牌才可调用。`);
}

// Graceful shutdown. `docker compose up -d --build` sends SIGTERM and waits;
// with no handler Node exits immediately, cutting in-flight HTTP responses, any
// provider call the worker is waiting on (already billed by the provider, never
// delivered), and leaving the WAL uncheckpointed.
let shuttingDown = false;
const GRACE_MS = Number(process.env.SHUTDOWN_GRACE_MS) || 25_000;
async function shutdown(signal) {
  if (shuttingDown) return;
  shuttingDown = true;
  console.log(`[shutdown] ${signal} — draining`);
  const hard = setTimeout(() => { console.error('[shutdown] grace expired — exiting'); process.exit(1); }, GRACE_MS);
  hard.unref?.();
  server.close();                       // stop accepting, keep serving in-flight
  await drainWorker(GRACE_MS - 5_000);  // let running generations finish
  try {
    db.exec('PRAGMA wal_checkpoint(TRUNCATE)');
    db.close();
  } catch (e) { console.error('[shutdown] db close:', e.message); }
  console.log('[shutdown] done');
  process.exit(0);
}
for (const sig of ['SIGTERM', 'SIGINT']) process.on(sig, () => { shutdown(sig); });

server.listen(PORT, () => console.log(`MuseFrame running → http://localhost:${PORT}`));
