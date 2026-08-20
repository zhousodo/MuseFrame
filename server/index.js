// MuseFrame server — API + static web app. Zero-framework Node http.
import './env.js'; // must stay first — populates process.env before adapters load
import http from 'node:http';
import { readFileSync, existsSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { db, q1, run, uuid, now } from './db.js';
import { seedCatalog, seedProducts } from './styles.js';
import { routes, authenticate, ApiError } from './api.js';
import { recoverJobs } from './jobs.js';

const ROOT = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const WEB = path.join(ROOT, 'web');
const PORT = process.env.PORT || 8787;

seedCatalog({ db, uuid, now, q1, run });
seedProducts({ q1, run, uuid });
recoverJobs();

const MIME = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8', '.png': 'image/png', '.jpg': 'image/jpeg',
  '.svg': 'image/svg+xml', '.json': 'application/json', '.woff2': 'font/woff2',
};

function readBody(req, limit) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on('data', (c) => {
      size += c.length;
      if (size > limit) { reject(new ApiError(413, 'ASSET_UNSUPPORTED', 'Payload too large.')); req.destroy(); return; }
      chunks.push(c);
    });
    req.on('end', () => resolve(Buffer.concat(chunks)));
    req.on('error', reject);
  });
}

const server = http.createServer(async (req, res) => {
  const requestId = `req_${uuid().slice(0, 8)}`;
  const url = new URL(req.url, `http://${req.headers.host}`);
  // CORS: the packaged mobile app calls from a WebView origin.
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Methods', 'GET, POST, PUT, PATCH, DELETE, OPTIONS');
  res.setHeader('Access-Control-Allow-Headers', 'Content-Type, Authorization, Idempotency-Key');
  if (req.method === 'OPTIONS') { res.writeHead(204).end(); return; }
  try {
    if (url.pathname.startsWith('/v1/')) {
      for (const r of routes) {
        if (r.method !== req.method) continue;
        const m = url.pathname.match(r.pattern);
        if (!m) continue;
        const user = authenticate(req, url);
        const raw = ['POST', 'PUT', 'PATCH'].includes(req.method) ? await readBody(req, 26 * 1024 * 1024) : null;
        let body = null;
        if (raw && (req.headers['content-type'] || '').includes('application/json')) {
          try { body = JSON.parse(raw.toString('utf8')); }
          catch { throw new ApiError(400, 'VALIDATION', 'Invalid JSON body.'); }
        }
        const result = r.handler({ req, res, url, user, params: m.slice(1), body, raw });
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
    if (file.startsWith(WEB) && existsSync(file) && statSync(file).isFile()) {
      res.writeHead(200, { 'Content-Type': MIME[path.extname(file)] || 'application/octet-stream' });
      res.end(readFileSync(file));
      return;
    }
    // SPA fallback
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end(readFileSync(path.join(WEB, 'index.html')));
  } catch (e) {
    const status = e instanceof ApiError ? e.status : 500;
    const code = e instanceof ApiError ? e.code : 'INTERNAL_ERROR';
    if (status >= 500) console.error(`[${requestId}]`, e);
    if (!res.writableEnded) {
      res.writeHead(status, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: { code, message: e.message, requestId, details: e.details } }));
    }
  }
});

server.listen(PORT, () => console.log(`MuseFrame running → http://localhost:${PORT}`));
