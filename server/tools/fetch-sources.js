// Fetch subject-matched, freely-licensed sample source photos.
// Landscape / street / object / pet come from Wikimedia Commons (CC-licensed,
// credits written to assets/licensed/CREDITS.md per spec §14.4). Portraits are
// synthesized with the image provider (no personality-rights questions).
import '../env.js';
import { writeFileSync, mkdirSync, appendFileSync, existsSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import jpeg from 'jpeg-js';
import { PNG } from 'pngjs';
import { decodeJpeg, encodeJpeg, resize } from '../engine/styleEngine.js';

const ROOT = path.dirname(path.dirname(path.dirname(fileURLToPath(import.meta.url))));
const DIR = path.join(ROOT, 'assets', 'licensed');
mkdirSync(DIR, { recursive: true });

// External HTTPS must go through the system proxy, which Node's fetch ignores —
// shell out to curl (proxy-aware via HTTPS_PROXY) for Wikimedia requests.
import { execFileSync } from 'node:child_process';
function curlBuf(url) {
  return execFileSync('curl', ['-sL', '-m', '90', '-A', 'MuseFrame-dev/0.1 (sample sourcing)', url],
    { maxBuffer: 64 * 1024 * 1024 });
}

async function commonsSearch(query) {
  const url = `https://commons.wikimedia.org/w/api.php?action=query&generator=search&gsrsearch=${encodeURIComponent(query)}&gsrnamespace=6&gsrlimit=10&prop=imageinfo&iiprop=url|extmetadata|size&iiurlwidth=1800&format=json`;
  const d = JSON.parse(curlBuf(url).toString('utf8'));
  const pages = Object.values(d?.query?.pages || {});
  return pages
    .map(p => {
      const ii = p.imageinfo?.[0];
      if (!ii) return null;
      const meta = ii.extmetadata || {};
      return {
        title: p.title,
        url: ii.thumburl || ii.url,
        width: ii.thumbwidth || ii.width,
        height: ii.thumbheight || ii.height,
        license: meta.LicenseShortName?.value || '?',
        artist: (meta.Artist?.value || '').replace(/<[^>]+>/g, '').trim().slice(0, 80),
        pageUrl: ii.descriptionshorturl || ii.descriptionurl,
      };
    })
    .filter(x => x && /jpe?g/i.test(x.url) && !/\.(pdf|djvu|tiff?)/i.test(x.title) && /CC|Public domain|CC0/i.test(x.license));
}

async function saveAsJpeg(buf, outFile) {
  let img;
  if (buf[0] === 0xFF && buf[1] === 0xD8) img = decodeJpeg(buf);
  else if (buf[0] === 0x89 && buf[1] === 0x50) {
    const png = PNG.sync.read(buf);
    img = { data: new Uint8ClampedArray(png.data.buffer, png.data.byteOffset, png.data.length), width: png.width, height: png.height };
  } else throw new Error('unsupported format');
  const k = 1600 / Math.max(img.width, img.height);
  if (k < 1) img = resize(img, Math.round(img.width * k), Math.round(img.height * k));
  writeFileSync(outFile, encodeJpeg(img, 90));
  return img;
}

async function fetchCommons(name, query) {
  const out = path.join(DIR, `${name}.jpg`);
  const results = await commonsSearch(query);
  const candidates = results.filter(r => Math.min(r.width, r.height) >= 700);
  for (const pick of candidates) {
    try {
      const buf = curlBuf(pick.url);
      const img = await saveAsJpeg(buf, out);
      appendFileSync(path.join(DIR, 'CREDITS.md'),
        `- ${name}.jpg — ${pick.title} · ${pick.license} · ${pick.artist || 'unknown artist'} · ${pick.pageUrl}\n`);
      console.log(`${name}.jpg ← Commons "${pick.title}" (${pick.license}) ${img.width}x${img.height}`);
      return;
    } catch { /* try next candidate */ }
  }
  console.error(`no usable commons result for ${name} (${query})`);
}

async function generatePortrait(name, prompt) {
  const out = path.join(DIR, `${name}.jpg`);
  console.log(`${name}.jpg ← generating (this can take minutes)…`);
  const res = await fetch(`${process.env.IMAGE_PROVIDER_BASE_URL}/v1/images/generations`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${process.env.IMAGE_PROVIDER_API_KEY}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ model: process.env.IMAGE_PROVIDER_MODEL || 'gpt-image-2', prompt, size: '1024x1536' }),
    signal: AbortSignal.timeout(420000),
  });
  const body = await res.json();
  if (!body?.data?.[0]?.b64_json) throw new Error(body?.error?.message || 'generation failed');
  const img = await saveAsJpeg(Buffer.from(body.data[0].b64_json, 'base64'), out);
  appendFileSync(path.join(DIR, 'CREDITS.md'), `- ${name}.jpg — synthetic portrait generated in-house (no real person)\n`);
  console.log(`${name}.jpg generated ${img.width}x${img.height}`);
}

if (!existsSync(path.join(DIR, 'CREDITS.md'))) {
  writeFileSync(path.join(DIR, 'CREDITS.md'), '# Sample source credits (spec §14.4 rights ledger)\n\n');
}

await fetchCommons('sample-landscape', 'misty mountain lake morning fog');
await fetchCommons('sample-street', 'city street night neon signs pedestrians photograph');
await fetchCommons('sample-object', 'still life photography fruit bowl table');
await fetchCommons('sample-pet', 'golden retriever dog head photograph');
await generatePortrait('sample-portrait-a',
  'Candid photorealistic portrait photo of a young East Asian woman with shoulder-length black hair and a cream linen shirt, standing by a sunlit window in a cafe, warm afternoon light on her face, natural skin texture, gentle genuine smile, documentary photography, shallow depth of field');
console.log('done.');
