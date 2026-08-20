// Style-card sample generator (spec §5.1: cards must show real, internally
// verified samples — not marketing gradients).
//
//   node server/tools/gen-samples.js            → local-engine samples for every
//                                                 style missing a cover (instant)
//   node server/tools/gen-samples.js --remote key1,key2
//                                              → provider-generated samples for
//                                                 the given internal keys (slow)
//
// Covers land in web/covers/{internal_key}.jpg and are picked up by the API
// automatically. Source photo: assets/licensed/sample-portrait.jpg.
import '../env.js';
import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { q } from '../db.js';
import { decodeJpeg, encodeJpeg, applyStyle, cropTo, resize } from '../engine/styleEngine.js';
import { createEdit, remoteConfig } from '../engine/remoteAdapter.js';

const ROOT = path.dirname(path.dirname(path.dirname(fileURLToPath(import.meta.url))));
const COVERS = path.join(ROOT, 'web', 'covers');
const LICENSED = path.join(ROOT, 'assets', 'licensed');
mkdirSync(COVERS, { recursive: true });

// Subject-matched sources (see fetch-sources.js; credits in CREDITS.md).
const SOURCE_FILES = {
  portrait: 'sample-portrait-a.jpg',
  landscape: 'sample-landscape.jpg',
  object: 'sample-object.jpg',
  street: 'sample-street.jpg',
  pet: 'sample-pet.jpg',
};
const sources = {};
for (const [kind, file] of Object.entries(SOURCE_FILES)) {
  const p = path.join(LICENSED, file);
  if (existsSync(p)) sources[kind] = decodeJpeg(readFileSync(p));
}
if (!sources.portrait) { console.error('Missing sample sources — run fetch-sources.js first.'); process.exit(1); }

// A style samples best on the subject it was made for: street/architecture
// tags win, otherwise the highest compatibility subject.
const SUBJ_TO_SOURCE = { person: 'portrait', pet: 'pet', landscape: 'landscape', object: 'object' };
function pickSourceKind(spec) {
  const tags = spec.identity.tags || [];
  if ((tags.includes('street') || tags.includes('architecture')) && sources.street) return 'street';
  const best = Object.entries(spec.compatibility.subjects).sort((a, b) => b[1] - a[1])[0][0];
  const kind = SUBJ_TO_SOURCE[best] || 'portrait';
  return sources[kind] ? kind : 'portrait';
}
const KIND_TO_SUBJECT = { portrait: 'person', pet: 'pet', landscape: 'landscape', object: 'object', street: 'object' };

const rows = q(`SELECT s.internal_key, v.spec FROM styles s
                JOIN style_versions v ON v.style_id = s.id AND v.status = 'published'
                  AND v.version = (SELECT MAX(version) FROM style_versions WHERE style_id = s.id AND status = 'published')
                WHERE s.status = 'published'`);

const remoteArg = process.argv.find(a => a.startsWith('--remote'));
const remoteKeys = remoteArg ? (process.argv[process.argv.indexOf(remoteArg) + 1] || '').split(',').filter(Boolean) : null;

function saveCover(key, img) {
  let out = cropTo(img, 4, 5);
  const s = 600 / out.height;
  out = resize(out, Math.round(out.width * s), 600);
  writeFileSync(path.join(COVERS, `${key}.jpg`), encodeJpeg(out, 82));
  console.log('cover written:', key, `${out.width}x${out.height}`);
}

// Downscaled per-source buffers for provider calls, built lazily.
const smallCache = {};
function smallFor(kind) {
  if (!smallCache[kind]) {
    const src = sources[kind];
    const k = 1024 / Math.max(src.width, src.height);
    const small = k < 1 ? resize(src, Math.round(src.width * k), Math.round(src.height * k)) : src;
    smallCache[kind] = { buf: encodeJpeg(small, 88), width: small.width, height: small.height };
  }
  return smallCache[kind];
}

if (remoteKeys) {
  if (!remoteConfig.enabled) { console.error('remote provider not configured'); process.exit(1); }
  const keys = remoteKeys.includes('all') ? rows.map(r => r.internal_key) : remoteKeys;
  const CONC = 3;
  let idx = 0;
  async function runOne() {
    while (idx < keys.length) {
      const key = keys[idx++];
      const row = rows.find(r => r.internal_key === key);
      if (!row) { console.error('unknown style key:', key); continue; }
      const spec = JSON.parse(row.spec);
      const kind = pickSourceKind(spec);
      const small = smallFor(kind);
      console.log(`remote sample for ${key} (source: ${kind}) — this can take a few minutes…`);
      try {
        const img = await createEdit({
          sourceJpeg: small.buf, sourceW: small.width, sourceH: small.height,
          spec, controls: { strength: 'balanced', fidelity: 'high', composition: 'keep' },
          output: { aspectRatio: '4:5' }, subjectType: KIND_TO_SUBJECT[kind],
        });
        saveCover(key, img);
      } catch (e) {
        console.error(`remote sample for ${key} failed: ${e.code || e.message}`);
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(CONC, keys.length) }, runOne));
} else {
  for (const row of rows) {
    const file = path.join(COVERS, `${row.internal_key}.jpg`);
    if (existsSync(file)) continue;
    const spec = JSON.parse(row.spec);
    const img = applyStyle(sources[pickSourceKind(spec)], spec, { strength: 'balanced', fidelity: 'high', composition: 'keep' }, { aspectRatio: '4:5' });
    saveCover(row.internal_key, img);
  }
}
console.log('done.');
