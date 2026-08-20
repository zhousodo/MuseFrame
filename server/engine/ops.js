// Pixel operations for the LocalStyleEngine. All ops mutate an {data, width, height}
// RGBA buffer in place. `amount`/`mix` values are pre-scaled by the strength control.

const clamp = (v) => (v < 0 ? 0 : v > 255 ? 255 : v);

// Deterministic per-pixel pseudo-random (stable across retries for the same source).
function noise(x, y, seed) {
  let h = (x * 374761393 + y * 668265263 + seed * 1442695040888963407) | 0;
  h = (h ^ (h >> 13)) * 1274126177;
  return (((h ^ (h >> 16)) >>> 0) % 1000) / 1000;
}

export function curve(img, { contrast = 0, brightness = 0, saturation = 0, gamma = 1 }) {
  const { data } = img;
  const lut = new Uint8Array(256);
  const c = 1 + contrast;
  for (let i = 0; i < 256; i++) {
    let v = i / 255;
    v = Math.pow(v, gamma);
    v = (v - 0.5) * c + 0.5 + brightness;
    lut[i] = clamp(Math.round(v * 255));
  }
  const sat = 1 + saturation;
  for (let i = 0; i < data.length; i += 4) {
    let r = lut[data[i]], g = lut[data[i + 1]], b = lut[data[i + 2]];
    if (sat !== 1) {
      const l = 0.299 * r + 0.587 * g + 0.114 * b;
      r = l + (r - l) * sat; g = l + (g - l) * sat; b = l + (b - l) * sat;
    }
    data[i] = clamp(r); data[i + 1] = clamp(g); data[i + 2] = clamp(b);
  }
}

// Split-tone grade: blend shadows/mids/highs toward target colors by luminance weight.
export function grade(img, { shadows, mids, highs, amount = 0.5 }) {
  const { data } = img;
  for (let i = 0; i < data.length; i += 4) {
    const r = data[i], g = data[i + 1], b = data[i + 2];
    const l = (0.299 * r + 0.587 * g + 0.114 * b) / 255;
    const wS = shadows ? Math.pow(1 - l, 2) : 0;
    const wH = highs ? Math.pow(l, 2) : 0;
    const wM = mids ? Math.max(0, 1 - wS - wH) : 0;
    let tr = 0, tg = 0, tb = 0, wt = 0;
    if (shadows) { tr += shadows[0] * wS; tg += shadows[1] * wS; tb += shadows[2] * wS; wt += wS; }
    if (mids) { tr += mids[0] * wM; tg += mids[1] * wM; tb += mids[2] * wM; wt += wM; }
    if (highs) { tr += highs[0] * wH; tg += highs[1] * wH; tb += highs[2] * wH; wt += wH; }
    if (!wt) continue;
    const a = amount * Math.min(1, wt);
    data[i] = clamp(r + (tr / wt - r) * a);
    data[i + 1] = clamp(g + (tg / wt - g) * a);
    data[i + 2] = clamp(b + (tb / wt - b) * a);
  }
}

export function duotone(img, { dark, light, amount = 0.8 }) {
  const { data } = img;
  for (let i = 0; i < data.length; i += 4) {
    const l = (0.299 * data[i] + 0.587 * data[i + 1] + 0.114 * data[i + 2]) / 255;
    const tr = dark[0] + (light[0] - dark[0]) * l;
    const tg = dark[1] + (light[1] - dark[1]) * l;
    const tb = dark[2] + (light[2] - dark[2]) * l;
    data[i] = clamp(data[i] + (tr - data[i]) * amount);
    data[i + 1] = clamp(data[i + 1] + (tg - data[i + 1]) * amount);
    data[i + 2] = clamp(data[i + 2] + (tb - data[i + 2]) * amount);
  }
}

export function tritone(img, { dark, mid, light, amount = 0.8 }) {
  const { data } = img;
  for (let i = 0; i < data.length; i += 4) {
    const l = (0.299 * data[i] + 0.587 * data[i + 1] + 0.114 * data[i + 2]) / 255;
    let tr, tg, tb;
    if (l < 0.5) {
      const t = l * 2;
      tr = dark[0] + (mid[0] - dark[0]) * t; tg = dark[1] + (mid[1] - dark[1]) * t; tb = dark[2] + (mid[2] - dark[2]) * t;
    } else {
      const t = (l - 0.5) * 2;
      tr = mid[0] + (light[0] - mid[0]) * t; tg = mid[1] + (light[1] - mid[1]) * t; tb = mid[2] + (light[2] - mid[2]) * t;
    }
    data[i] = clamp(data[i] + (tr - data[i]) * amount);
    data[i + 1] = clamp(data[i + 1] + (tg - data[i + 1]) * amount);
    data[i + 2] = clamp(data[i + 2] + (tb - data[i + 2]) * amount);
  }
}

export function posterize(img, { levels = 5, amount = 1 }) {
  const { data } = img;
  const step = 255 / (levels - 1);
  for (let i = 0; i < data.length; i += 4) {
    for (let k = 0; k < 3; k++) {
      const v = data[i + k];
      const p = Math.round(v / step) * step;
      data[i + k] = clamp(v + (p - v) * amount);
    }
  }
}

// Map toward nearest color of a fixed palette (Paper Cut / Color Block).
export function palette(img, { colors, amount = 0.7 }) {
  const { data } = img;
  for (let i = 0; i < data.length; i += 4) {
    const r = data[i], g = data[i + 1], b = data[i + 2];
    let best = null, bd = Infinity;
    for (const c of colors) {
      const d = (r - c[0]) ** 2 + (g - c[1]) ** 2 + (b - c[2]) ** 2;
      if (d < bd) { bd = d; best = c; }
    }
    data[i] = clamp(r + (best[0] - r) * amount);
    data[i + 1] = clamp(g + (best[1] - g) * amount);
    data[i + 2] = clamp(b + (best[2] - b) * amount);
  }
}

export function fade(img, { lift = 0.08 }) {
  const { data } = img;
  const add = lift * 255;
  for (let i = 0; i < data.length; i += 4) {
    for (let k = 0; k < 3; k++) {
      const v = data[i + k];
      data[i + k] = clamp(v + add * (1 - v / 255));
    }
  }
}

export function grain(img, { amount = 0.08, seed = 7 }) {
  const { data, width } = img;
  const a = amount * 255;
  for (let i = 0; i < data.length; i += 4) {
    const p = i >> 2, x = p % width, y = (p / width) | 0;
    const n = (noise(x, y, seed) - 0.5) * 2 * a;
    data[i] = clamp(data[i] + n); data[i + 1] = clamp(data[i + 1] + n); data[i + 2] = clamp(data[i + 2] + n);
  }
}

export function vignette(img, { amount = 0.3 }) {
  const { data, width, height } = img;
  const cx = width / 2, cy = height / 2;
  const maxD = Math.sqrt(cx * cx + cy * cy);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const d = Math.sqrt((x - cx) ** 2 + (y - cy) ** 2) / maxD;
      const f = 1 - amount * d * d;
      const i = (y * width + x) * 4;
      data[i] *= f; data[i + 1] *= f; data[i + 2] *= f;
    }
  }
}

// Separable box blur into a copy; returns new buffer (used by soften/bloom).
function boxBlur(img, radius) {
  const { data, width, height } = img;
  const out = new Uint8ClampedArray(data.length);
  const tmp = new Float32Array(width * height * 3);
  const r = Math.max(1, Math.round(radius));
  // horizontal
  for (let y = 0; y < height; y++) {
    let sr = 0, sg = 0, sb = 0, cnt = 0;
    for (let x = -r; x <= r; x++) {
      const xi = Math.min(width - 1, Math.max(0, x));
      const i = (y * width + xi) * 4;
      sr += data[i]; sg += data[i + 1]; sb += data[i + 2]; cnt++;
    }
    for (let x = 0; x < width; x++) {
      const t = (y * width + x) * 3;
      tmp[t] = sr / cnt; tmp[t + 1] = sg / cnt; tmp[t + 2] = sb / cnt;
      const xOut = Math.min(width - 1, Math.max(0, x - r));
      const xIn = Math.min(width - 1, Math.max(0, x + r + 1));
      const io = (y * width + xOut) * 4, ii = (y * width + xIn) * 4;
      sr += data[ii] - data[io]; sg += data[ii + 1] - data[io + 1]; sb += data[ii + 2] - data[io + 2];
    }
  }
  // vertical
  for (let x = 0; x < width; x++) {
    let sr = 0, sg = 0, sb = 0, cnt = 0;
    for (let y = -r; y <= r; y++) {
      const yi = Math.min(height - 1, Math.max(0, y));
      const t = (yi * width + x) * 3;
      sr += tmp[t]; sg += tmp[t + 1]; sb += tmp[t + 2]; cnt++;
    }
    for (let y = 0; y < height; y++) {
      const o = (y * width + x) * 4;
      out[o] = sr / cnt; out[o + 1] = sg / cnt; out[o + 2] = sb / cnt; out[o + 3] = 255;
      const yOut = Math.min(height - 1, Math.max(0, y - r));
      const yIn = Math.min(height - 1, Math.max(0, y + r + 1));
      const to = (yOut * width + x) * 3, ti = (yIn * width + x) * 3;
      sr += tmp[ti] - tmp[to]; sg += tmp[ti + 1] - tmp[to + 1]; sb += tmp[ti + 2] - tmp[to + 2];
    }
  }
  return out;
}

export function soften(img, { radius = 2, mix = 0.35 }) {
  const blurred = boxBlur(img, radius);
  const { data } = img;
  for (let i = 0; i < data.length; i += 4) {
    data[i] += (blurred[i] - data[i]) * mix;
    data[i + 1] += (blurred[i + 1] - data[i + 1]) * mix;
    data[i + 2] += (blurred[i + 2] - data[i + 2]) * mix;
  }
}

// Bloom: screen-blend a blurred bright-pass over the image.
export function bloom(img, { radius = 8, mix = 0.25 }) {
  const blurred = boxBlur(img, radius);
  const { data } = img;
  for (let i = 0; i < data.length; i += 4) {
    for (let k = 0; k < 3; k++) {
      const base = data[i + k], bl = blurred[i + k];
      const bright = Math.max(0, bl - 96) * 1.6;
      const screened = 255 - ((255 - base) * (255 - bright)) / 255;
      data[i + k] = clamp(base + (screened - base) * mix);
    }
  }
}

// Halftone: luminance-driven dot mask multiplied into the image.
export function halftone(img, { cell = 4, amount = 0.5 }) {
  const { data, width, height } = img;
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      const l = (0.299 * data[i] + 0.587 * data[i + 1] + 0.114 * data[i + 2]) / 255;
      const cx = (x % cell) - cell / 2 + 0.5, cy = (y % cell) - cell / 2 + 0.5;
      const d = Math.sqrt(cx * cx + cy * cy) / (cell / 2);
      const dot = d < (1 - l) * 1.15 ? 0.72 : 1.06; // dark dot where image is dark
      const f = 1 + (dot - 1) * amount;
      data[i] = clamp(data[i] * f); data[i + 1] = clamp(data[i + 1] * f); data[i + 2] = clamp(data[i + 2] * f);
    }
  }
}

// Woven fabric mask — alternating warp/weft brightness.
export function weave(img, { cell = 3, amount = 0.35 }) {
  const { data, width, height } = img;
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      const wx = ((x / cell) | 0) % 2, wy = ((y / cell) | 0) % 2;
      const ridge = (wx ^ wy) ? 1.05 : 0.94;
      const sub = (x % cell === 0 || y % cell === 0) ? 0.97 : 1;
      const f = 1 + (ridge * sub - 1) * amount;
      data[i] = clamp(data[i] * f); data[i + 1] = clamp(data[i + 1] * f); data[i + 2] = clamp(data[i + 2] * f);
    }
  }
}

// Uncoated paper: warm tint + fiber noise + faint horizontal lines.
export function paper(img, { amount = 0.5, seed = 3 }) {
  const { data, width } = img;
  for (let i = 0; i < data.length; i += 4) {
    const p = i >> 2, x = p % width, y = (p / width) | 0;
    const fiber = (noise(x, y, seed) - 0.5) * 14 + (y % 7 === 0 ? -4 : 0);
    const f = fiber * amount;
    data[i] = clamp(data[i] + f + 3 * amount);
    data[i + 1] = clamp(data[i + 1] + f + 1.5 * amount);
    data[i + 2] = clamp(data[i + 2] + f - 3 * amount);
  }
}

// Chromatic offset: shift red left / blue right horizontally.
export function chromaOffset(img, { shift = 3, amount = 0.6 }) {
  const { data, width, height } = img;
  const src = new Uint8ClampedArray(data);
  const s = Math.max(1, Math.round(shift));
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      const xr = Math.min(width - 1, x + s), xb = Math.max(0, x - s);
      const ir = (y * width + xr) * 4, ib = (y * width + xb) * 4;
      data[i] = clamp(src[i] + (src[ir] - src[i]) * amount);
      data[i + 2] = clamp(src[i + 2] + (src[ib + 2] - src[i + 2]) * amount);
    }
  }
}

export const OPS = {
  curve, grade, duotone, tritone, posterize, palette, fade, grain,
  vignette, soften, bloom, halftone, weave, paper, chromaOffset,
};
