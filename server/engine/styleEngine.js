// LocalStyleEngine — the MVP Model Adapter (spec §9.3: fake adapter with real,
// deterministic output). Decodes a JPEG source, applies the StyleSpec pipeline
// honoring the three user controls, re-encodes. Also hosts the photo-analysis
// heuristics (spec §6.7 adapter, analyzer_version heuristic-0.1).
import jpeg from 'jpeg-js';
import { OPS } from './ops.js';

const STRENGTH = { soft: 0.62, balanced: 1.0, bold: 1.35 };
const FIDELITY_BLEND = { high: 0.32, natural: 0.12 }; // share of original blended back
const MAX_EDGE = 1600;

export function decodeJpeg(buf) {
  const raw = jpeg.decode(buf, { useTArray: true, maxMemoryUsageInMB: 512, maxResolutionInMP: 60 });
  return { data: new Uint8ClampedArray(raw.data.buffer, raw.data.byteOffset, raw.data.length), width: raw.width, height: raw.height };
}

export function encodeJpeg(img, quality = 90) {
  return jpeg.encode({ data: Buffer.from(img.data.buffer, img.data.byteOffset, img.data.length), width: img.width, height: img.height }, quality).data;
}

export function resize(img, tw, th) {
  const { data, width, height } = img;
  const out = new Uint8ClampedArray(tw * th * 4);
  const xr = width / tw, yr = height / th;
  for (let y = 0; y < th; y++) {
    const sy = Math.min(height - 1.001, y * yr);
    const y0 = sy | 0, fy = sy - y0, y1 = Math.min(height - 1, y0 + 1);
    for (let x = 0; x < tw; x++) {
      const sx = Math.min(width - 1.001, x * xr);
      const x0 = sx | 0, fx = sx - x0, x1 = Math.min(width - 1, x0 + 1);
      const i00 = (y0 * width + x0) * 4, i10 = (y0 * width + x1) * 4;
      const i01 = (y1 * width + x0) * 4, i11 = (y1 * width + x1) * 4;
      const o = (y * tw + x) * 4;
      for (let k = 0; k < 4; k++) {
        const top = data[i00 + k] + (data[i10 + k] - data[i00 + k]) * fx;
        const bot = data[i01 + k] + (data[i11 + k] - data[i01 + k]) * fx;
        out[o + k] = top + (bot - top) * fy;
      }
    }
  }
  return { data: out, width: tw, height: th };
}

export function cropTo(img, ratioW, ratioH, zoom = 1) {
  const { width, height } = img;
  let cw = width, ch = Math.round(width * ratioH / ratioW);
  if (ch > height) { ch = height; cw = Math.round(height * ratioW / ratioH); }
  cw = Math.round(cw / zoom); ch = Math.round(ch / zoom);
  const x0 = (width - cw) >> 1, y0 = (height - ch) >> 1;
  const out = new Uint8ClampedArray(cw * ch * 4);
  for (let y = 0; y < ch; y++) {
    const src = ((y0 + y) * width + x0) * 4;
    out.set(img.data.subarray(src, src + cw * 4), y * cw * 4);
  }
  return { data: out, width: cw, height: ch };
}

const RATIOS = { '1:1': [1, 1], '4:5': [4, 5], '16:9': [16, 9] };

// Scale style intensity by the strength control. Amount-like params scale
// linearly; curve params scale toward neutral.
function scaleParams(op, params, k) {
  const p = { ...params };
  if (op === 'curve') {
    p.contrast = (p.contrast ?? 0) * k;
    p.brightness = (p.brightness ?? 0) * k;
    p.saturation = Math.max(-1, (p.saturation ?? 0) * k);
    p.gamma = 1 + ((p.gamma ?? 1) - 1) * k;
  } else {
    for (const key of ['amount', 'mix', 'lift']) if (p[key] != null) p[key] = Math.min(1, p[key] * k);
  }
  return p;
}

/**
 * Apply a StyleSpec to a decoded source image.
 * controls: {strength, fidelity, composition}; output: {aspectRatio}
 */
export function applyStyle(source, spec, controls, output) {
  const k = STRENGTH[controls.strength] ?? 1;
  let img = source;

  // Composition + output ratio (crop before the pipeline; reframe = 12% center zoom).
  const zoom = controls.composition === 'reframe' ? 1.12 : 1;
  const ratio = RATIOS[output?.aspectRatio];
  if (ratio) img = cropTo(img, ratio[0], ratio[1], zoom);
  else if (zoom !== 1) img = cropTo(img, img.width, img.height, zoom);
  else img = { data: new Uint8ClampedArray(img.data), width: img.width, height: img.height };

  if (Math.max(img.width, img.height) > MAX_EDGE) {
    const s = MAX_EDGE / Math.max(img.width, img.height);
    img = resize(img, Math.round(img.width * s), Math.round(img.height * s));
  }

  const original = new Uint8ClampedArray(img.data);

  for (const [op, params] of spec.pipeline) {
    const fn = OPS[op];
    if (!fn) throw new Error(`Unknown pipeline op: ${op}`);
    fn(img, scaleParams(op, params, k));
  }

  // Subject fidelity: blend a share of the original back (identity preservation).
  const blend = FIDELITY_BLEND[controls.fidelity] ?? 0.32;
  if (blend > 0) {
    const d = img.data;
    for (let i = 0; i < d.length; i += 4) {
      d[i] += (original[i] - d[i]) * blend;
      d[i + 1] += (original[i + 1] - d[i + 1]) * blend;
      d[i + 2] += (original[i + 2] - d[i + 2]) * blend;
    }
  }
  return img;
}

// ---- Photo analysis heuristics (spec §6.7) -------------------------------

export function analyzeImage(img) {
  const { data, width, height } = img;
  const step = Math.max(1, Math.floor(Math.sqrt((width * height) / 40000))); // ~40k samples
  let n = 0, sumL = 0, sumL2 = 0, skin = 0, sky = 0, green = 0, satSum = 0;
  let lapSum = 0, lapN = 0;
  for (let y = 0; y < height; y += step) {
    for (let x = 0; x < width; x += step) {
      const i = (y * width + x) * 4;
      const r = data[i], g = data[i + 1], b = data[i + 2];
      const l = 0.299 * r + 0.587 * g + 0.114 * b;
      sumL += l; sumL2 += l * l; n++;
      const mx = Math.max(r, g, b), mn = Math.min(r, g, b);
      satSum += mx === 0 ? 0 : (mx - mn) / mx;
      if (r > 95 && g > 40 && b > 20 && r > g && r > b && (r - Math.min(g, b)) > 15) skin++;
      if (b > 140 && b > r + 20 && b >= g) sky++;
      if (g > 90 && g > r + 12 && g > b + 12) green++;
      if (x + step < width && y + step < height) {
        const ix = (y * width + Math.min(width - 1, x + step)) * 4;
        const iy = (Math.min(height - 1, y + step) * width + x) * 4;
        const lx = 0.299 * data[ix] + 0.587 * data[ix + 1] + 0.114 * data[ix + 2];
        const ly = 0.299 * data[iy] + 0.587 * data[iy + 1] + 0.114 * data[iy + 2];
        lapSum += Math.abs(2 * l - lx - ly); lapN++;
      }
    }
  }
  const meanL = sumL / n / 255;
  const stdL = Math.sqrt(Math.max(0, sumL2 / n - (sumL / n) ** 2)) / 255;
  const sharpness = Math.min(1, (lapSum / lapN) / 24);
  const skinF = skin / n, skyF = sky / n, greenF = green / n;
  const isPortraitFrame = height >= width * 0.95;

  let subjectType = 'object';
  if (skinF > 0.06 && isPortraitFrame) subjectType = 'person';
  else if (skinF > 0.14) subjectType = 'person';
  else if (skyF + greenF > 0.22 && !isPortraitFrame) subjectType = 'landscape';
  else if (skyF + greenF > 0.35) subjectType = 'landscape';

  const warnings = [];
  if (meanL < 0.18) warnings.push('LOW_LIGHT');
  if (meanL > 0.86) warnings.push('OVEREXPOSED');
  if (sharpness < 0.12) warnings.push('LOW_SHARPNESS');
  if (Math.min(img.width, img.height) < 768) warnings.push('LOW_RESOLUTION');

  return {
    subjectType,
    personCount: subjectType === 'person' ? 1 : 0,
    exposure: +meanL.toFixed(4),
    contrast: +stdL.toFixed(4),
    sharpness: +sharpness.toFixed(4),
    saturation: +(satSum / n).toFixed(4),
    warnings,
  };
}

const REASON_BY_TAG = {
  portrait: 'PRESERVES_SOFT_FACE_LIGHT',
  pet: 'GENTLE_ON_FUR_TEXTURE',
  landscape: 'SUITS_OPEN_DISTANCE',
  object: 'STRONG_SILHOUETTE_MATCH',
  architecture: 'SUITS_HARD_EDGES',
  street: 'HOLDS_SCENE_MOOD',
};

// Rank styles for a photo: 0.40 compatibility + 0.25 save-rate prior +
// 0.20 novelty + 0.15 editorial priority (spec §6.8 formula, global priors).
export function recommendStyles(analysis, styleRows) {
  const subj = { person: 'person', landscape: 'landscape', object: 'object', pet: 'pet' }[analysis.subjectType] || 'object';
  return styleRows
    .map((s, i) => {
      const spec = s.spec;
      const compat = spec.compatibility.subjects[subj] ?? 0.5;
      const novelty = ((i * 2654435761) % 100) / 100; // stable pseudo-novelty
      const editorial = 1 - (s.editorialRank ?? i) / styleRows.length;
      const score = 0.40 * compat + 0.25 * 0.5 + 0.20 * novelty + 0.15 * editorial;
      const tag = (spec.identity.tags || []).find(t => REASON_BY_TAG[t]) || 'portrait';
      return { styleId: s.styleId, styleVersionId: s.versionId, score: +score.toFixed(4), reasonCode: REASON_BY_TAG[tag] };
    })
    .sort((a, b) => b.score - a.score);
}
