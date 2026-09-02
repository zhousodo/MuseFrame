// Boot the real server in a child process against a throwaway data dir, so the
// HTTP-level findings (413 delivery, per-route body limits, auth on /v1/events,
// admin token placement) are tested through the actual request path rather than
// against a re-implementation of it.
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import net from 'node:net';

const ROOT = path.dirname(path.dirname(path.dirname(fileURLToPath(import.meta.url))));

function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.once('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}

export async function startServer(env = {}) {
  const port = await freePort();
  const dataDir = mkdtempSync(path.join(tmpdir(), 'mf-srv-'));
  const child = spawn(process.execPath, [path.join(ROOT, 'server', 'index.js')], {
    cwd: ROOT,
    env: {
      ...process.env,
      PORT: String(port),
      MUSEFRAME_DATA_DIR: dataDir,
      ADMIN_TOKEN: 'test-admin-token',
      // Keep the dev hatches off unless a test opts in: their whole point is
      // that they must not be reachable without the operator's token.
      ALLOW_TEST_LOGIN: 'false',
      ALLOW_MOCK_PURCHASES: 'false',
      IMAGE_PROVIDER: 'local',
      ...env,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  const logs = [];
  child.stdout.on('data', (d) => logs.push(String(d)));
  child.stderr.on('data', (d) => logs.push(String(d)));

  const base = `http://127.0.0.1:${port}`;
  const deadline = Date.now() + 30_000;
  for (;;) {
    if (child.exitCode !== null) throw new Error(`server exited (${child.exitCode}):\n${logs.join('')}`);
    try {
      const res = await fetch(`${base}/v1/health`, { signal: AbortSignal.timeout(1500) });
      if (res.ok) break;
    } catch { /* not up yet */ }
    if (Date.now() > deadline) throw new Error(`server did not start:\n${logs.join('')}`);
    await new Promise((r) => setTimeout(r, 150));
  }

  return {
    base, port, child, dataDir,
    logs: () => logs.join(''),
    async stop() {
      child.kill();
      await new Promise((r) => { child.once('exit', r); setTimeout(r, 3000); });
      try { rmSync(dataDir, { recursive: true, force: true }); } catch { /* windows file locks */ }
    },
  };
}

/** Mint a guest session and return its bearer token. */
export async function guestToken(base, deviceId = 'dev-' + Math.random().toString(36).slice(2)) {
  const res = await fetch(`${base}/v1/auth/exchange`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ provider: 'guest', deviceId }),
  });
  const body = await res.json();
  if (!body.accessToken) throw new Error('no session: ' + JSON.stringify(body));
  return body.accessToken;
}
