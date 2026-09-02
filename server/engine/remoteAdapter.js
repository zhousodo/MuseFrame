// RemoteImageAdapter — primary model adapter (spec §9.3). Talks to an
// OpenAI-compatible /v1/images/edits endpoint (image-to-image) with the source
// photo and a prompt assembled from the StyleSpec. The client never sees the
// provider. The LocalStyleEngine backup is opt-in (local_engine_fallback) and
// never stands in for an unconfigured provider — see generationStatus() below.
import { PNG } from 'pngjs';
import jpeg from 'jpeg-js';
import { cfg } from '../configStore.js';

// IMAGE_PROVIDER stays an env-only switch (not runtime-configurable — changing
// providers is an ops call). It defaults to 'remote': an unset or blank value
// must NOT silently downgrade the service to the local pixel engine, which is a
// filter pass, not the model. Only an explicit IMAGE_PROVIDER=local does that.
export function imageProvider() {
  return (process.env.IMAGE_PROVIDER || 'remote').trim().toLowerCase() || 'remote';
}

// Live getters — admin overrides (base URL, key, model, timeout) apply on the
// next request, no restart needed.
export const remoteConfig = {
  // Both the key and the endpoint are required: a key with no base URL would
  // fail every call and (before the gate below) fall through to the local engine.
  get enabled() { return imageProvider() === 'remote' && !!this.apiKey && !!this.baseUrl; },
  get baseUrl() { return (cfg('image_provider_base_url') || '').replace(/\/$/, ''); },
  get apiKey() { return cfg('image_provider_api_key') || ''; },
  get model() { return cfg('image_provider_model') || 'gpt-image-2'; },
  // Node's fetch (undici) enforces its own 300 s headersTimeout, and nothing
  // here can raise it without adding undici as a dependency. A configured 420 s
  // was therefore fiction: the socket died at 300 s while the provider kept
  // generating — and billing — an image nobody ever received, and the failure
  // surfaced as an opaque PROVIDER_ERROR rather than a timeout. Clamp to what
  // the runtime will honour so the admin panel, the "1–5 min" estimate shown to
  // users and the code all agree. See AUDIT-2026-09-02.md for the owner
  // decision if generations longer than ~5 minutes are ever needed.
  get timeoutMs() {
    const ceiling = Number(process.env.UNDICI_HEADERS_TIMEOUT_MS) || 290_000;
    return Math.min(Number(cfg('image_provider_timeout_ms')) || 420_000, ceiling);
  },
};

/**
 * Single source of truth for "may this deployment produce an image at all?".
 * Checked by the API before a job is accepted (no units reserved) and again by
 * the worker before a job runs (covers jobs queued before the key was cleared).
 *
 *   mode 'remote' — configured paid model
 *   mode 'local'  — operator explicitly set IMAGE_PROVIDER=local (dev/offline)
 *   mode 'none'   — nothing is configured; generation is refused
 */
export function generationStatus() {
  const provider = imageProvider();
  if (provider === 'local') {
    return { available: true, provider, mode: 'local', missing: [], reason: null };
  }
  if (provider !== 'remote') {
    return { available: false, provider, mode: 'none', missing: [], reason: 'PROVIDER_UNKNOWN' };
  }
  const missing = [];
  if (!remoteConfig.apiKey) missing.push('image_provider_api_key');
  if (!remoteConfig.baseUrl) missing.push('image_provider_base_url');
  if (missing.length) return { available: false, provider, mode: 'none', missing, reason: 'PROVIDER_NOT_CONFIGURED' };
  return { available: true, provider, mode: 'remote', missing: [], reason: null };
}

/**
 * Assemble the provider-agnostic instruction from the StyleSpec (spec §11.2).
 * Never includes vendor-specific fields; safe to log lengths, never content.
 */
export function buildInstruction(spec, controls, subjectType) {
  const pa = spec.promptAssembly;
  if (!pa?.baseDirection) return null;
  const parts = [pa.baseDirection];
  const subj = pa.subjectRules[subjectType] || pa.subjectRules.person;
  parts.push(subj);
  parts.push(pa.controlFragments.strength[controls.strength] || pa.controlFragments.strength.balanced);
  parts.push(pa.controlFragments.fidelity[controls.fidelity] || pa.controlFragments.fidelity.high);
  parts.push(pa.controlFragments.composition[controls.composition] || pa.controlFragments.composition.keep);
  parts.push(pa.negativeConstraints.join(' '));
  return parts.join(' ');
}

// Provider size grid → pick by requested ratio / source orientation.
function pickSize(aspectRatio, srcW, srcH) {
  if (aspectRatio === '1:1') return '1024x1024';
  if (aspectRatio === '16:9') return '1536x1024';
  if (aspectRatio === '4:5') return '1024x1536';
  // 'original': follow source orientation
  if (srcW > srcH * 1.15) return '1536x1024';
  if (srcH > srcW * 1.15) return '1024x1536';
  return '1024x1024';
}

/**
 * Run one image-to-image edit. Returns a decoded RGBA image
 * {data, width, height, usage} or throws {code} on provider failure.
 */
export async function createEdit({ sourceJpeg, sourceW, sourceH, spec, controls, output, subjectType, instruction: precompiled }) {
  const instruction = precompiled || buildInstruction(spec, controls, subjectType);
  if (!instruction) { const e = new Error('StyleSpec has no promptAssembly'); e.code = 'STYLE_UNAVAILABLE'; throw e; }

  const form = new FormData();
  form.append('model', remoteConfig.model);
  form.append('image', new Blob([sourceJpeg], { type: 'image/jpeg' }), 'source.jpg');
  form.append('prompt', instruction);
  form.append('size', pickSize(output?.aspectRatio, sourceW, sourceH));

  let res;
  try {
    res = await fetch(`${remoteConfig.baseUrl}/v1/images/edits`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${remoteConfig.apiKey}` },
      body: form,
      signal: AbortSignal.timeout(remoteConfig.timeoutMs),
    });
  } catch (err) {
    const e = new Error(`provider unreachable: ${err.name}`);
    // undici reports its own header/body deadlines through err.cause, not as a
    // TimeoutError — those were being misfiled as generic PROVIDER_ERROR.
    const undiciTimeout = ['UND_ERR_HEADERS_TIMEOUT', 'UND_ERR_BODY_TIMEOUT'].includes(err.cause?.code);
    e.code = (err.name === 'TimeoutError' || undiciTimeout) ? 'PROVIDER_TIMEOUT' : 'PROVIDER_ERROR';
    throw e;
  }

  const body = await res.json().catch(() => ({}));
  if (!res.ok || !body?.data?.[0]?.b64_json) {
    // Policy refusals come back as 4xx with a message — map to a stable code.
    const msg = body?.error?.message || `HTTP ${res.status}`;
    const e = new Error(msg);
    e.code = /safety|policy|content|reject/i.test(msg) ? 'GENERATION_REJECTED'
      : res.status >= 500 || /account|unavailable|timeout/i.test(msg) ? 'PROVIDER_TIMEOUT'
      : 'PROVIDER_ERROR';
    throw e;
  }

  const bin = Buffer.from(body.data[0].b64_json, 'base64');
  const img = decodeAny(bin);
  img.usage = body.usage || null;
  return img;
}

function decodeAny(buf) {
  if (buf[0] === 0x89 && buf[1] === 0x50) { // PNG
    const png = PNG.sync.read(buf);
    return { data: new Uint8ClampedArray(png.data.buffer, png.data.byteOffset, png.data.length), width: png.width, height: png.height };
  }
  if (buf[0] === 0xFF && buf[1] === 0xD8) { // JPEG
    const raw = jpeg.decode(buf, { useTArray: true, maxMemoryUsageInMB: 512, maxResolutionInMP: 60 });
    return { data: new Uint8ClampedArray(raw.data.buffer, raw.data.byteOffset, raw.data.length), width: raw.width, height: raw.height };
  }
  const e = new Error('provider returned an unknown image format');
  e.code = 'PROVIDER_ERROR';
  throw e;
}
