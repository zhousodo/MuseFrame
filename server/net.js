// Transport-level policy that has to be decided before any handler runs: which
// address this request really came from, and how many bytes it is allowed to
// send. Split out of index.js so it can be tested without booting a server.

// ---- request body ceilings --------------------------------------------------
// Every POST/PUT/PATCH used to be allowed 26 MB — unauthenticated, unbounded in
// parallel, buffered whole in RAM, inside a 512 MB container: a handful of
// concurrent bodies was an OOM, and /v1/events accepted them without a session.
// Only the route that actually carries a photo gets the big limit now.
export const MAX_UPLOAD_BODY = 26 * 1024 * 1024;
export const MAX_ADMIN_BODY = 1024 * 1024;   // style specs / config blobs
export const MAX_JSON_BODY = 64 * 1024;      // every ordinary /v1 JSON call

export function bodyLimitFor(method, pathname) {
  if (method === 'PUT' && /^\/v1\/assets\/[\w-]+\/upload$/.test(pathname)) return MAX_UPLOAD_BODY;
  if (pathname.startsWith('/v1/admin/')) return MAX_ADMIN_BODY;
  return MAX_JSON_BODY;
}

// ---- client address ---------------------------------------------------------
/**
 * Which address do the rate limiter and the free-grant IP cap key on?
 *
 * `cf-connecting-ip` and the FIRST element of `x-forwarded-for` are both
 * attacker-supplied. Sending a fresh random value per request removed every
 * per-IP limit on this service — including the five-codes-per-ten-minutes brake
 * on /v1/auth/email/request, i.e. an open mail bomb against any address.
 *
 * The rule now: a header is consulted only when the TCP peer is itself a
 * trusted proxy, and then only the RIGHTMOST x-forwarded-for entry — the one
 * that proxy appended, and therefore the only one a client cannot choose.
 * `cf-connecting-ip` is ignored unless TRUST_CF_CONNECTING_IP=true, which is
 * safe only once the edge strips any client-supplied copy.
 *
 * TRUSTED_PROXY:
 *   'private' (default) loopback + RFC1918 + ULA/link-local. This is exactly
 *              the deployment: Caddy on the host reaches 127.0.0.1:8787, so the
 *              container's peer is the docker bridge gateway, never a public
 *              address.
 *   'none'     trust nothing; always use the socket address.
 *   '*'        trust every peer (only behind a proxy you fully control).
 *   a,b,c      exact peer addresses to trust.
 */
export function makeClientIpResolver(env = process.env) {
  const setting = (env.TRUSTED_PROXY ?? 'private').trim();
  const trustCf = env.TRUST_CF_CONNECTING_IP === 'true';
  const list = setting.split(',').map((s) => s.trim()).filter(Boolean);

  const isTrustedPeer = (addr) => {
    const ip = bare(addr);
    if (setting === 'none' || !ip) return false;
    if (setting === '*') return true;
    if (setting !== 'private') return list.includes(ip);
    return ip.startsWith('127.') || ip === '::1'
      || ip.startsWith('10.') || ip.startsWith('192.168.')
      || /^172\.(1[6-9]|2\d|3[01])\./.test(ip)
      || ip.startsWith('fd') || ip.startsWith('fc') || ip.startsWith('fe80:');
  };

  const resolve = (req) => {
    const peer = bare(req?.socket?.remoteAddress);
    if (!isTrustedPeer(peer)) return peer;
    const h = req.headers || {};
    if (trustCf && h['cf-connecting-ip']) return bare(String(h['cf-connecting-ip']).trim());
    if (h['x-forwarded-for']) {
      const parts = String(h['x-forwarded-for']).split(',').map((s) => s.trim()).filter(Boolean);
      // Rightmost = appended by the trusted proxy = the only entry a client
      // cannot choose. The old code took parts[0], which is pure client input.
      if (parts.length) return bare(parts[parts.length - 1]);
    }
    if (h['x-real-ip']) return bare(String(h['x-real-ip']).trim());
    return peer;
  };
  resolve.isTrustedPeer = isTrustedPeer;
  return resolve;
}

const bare = (ip) => String(ip || '').replace(/^::ffff:/, '');

/** Bound to this process's environment; the resolver used by the server. */
export const resolveClientIp = makeClientIpResolver();
