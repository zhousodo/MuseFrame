// RemoteImageAdapter — primary model adapter (spec §9.3). Talks to an
// OpenAI-compatible /v1/images/edits endpoint (image-to-image) with the source
// photo and a prompt assembled from the StyleSpec. The client never sees the
// provider; the worker falls back to the LocalStyleEngine backup on failure.
import { PNG } from 'pngjs';
import jpeg from 'jpeg-js';

export const remoteConfig = {
  enabled: process.env.IMAGE_PROVIDER === 'remote' && !!process.env.IMAGE_PROVIDER_API_KEY,
  baseUrl: (process.env.IMAGE_PROVIDER_BASE_URL || '').replace(/\/$/, ''),
  apiKey: process.env.IMAGE_PROVIDER_API_KEY || '',
  model: process.env.IMAGE_PROVIDER_MODEL || 'gpt-image-2',
  timeoutMs: Number(process.env.IMAGE_PROVIDER_TIMEOUT_MS || 180000),
};

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
    e.code = err.name === 'TimeoutError' ? 'PROVIDER_TIMEOUT' : 'PROVIDER_ERROR';
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
