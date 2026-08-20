// MuseFrame web app — curated-gallery P0 flow (spec §6):
// Onboarding → Discover → Exhibition → Styles → Preview settings → Progress → Result,
// plus Projects, Profile, Paywall. Vanilla DOM, no build step.
import { ensureSession, get, post, put, del, assetUrl, apiUrl, track } from './api.js';

// ---------- tiny DOM helper ----------
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
    else if (k.startsWith('on')) el.addEventListener(k.slice(2).toLowerCase(), v);
    else if (k === 'html') el.innerHTML = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue;
    el.append(c.nodeType ? c : document.createTextNode(c));
  }
  return el;
}
const app = document.getElementById('app');
const overlayRoot = document.getElementById('overlay');

// ---------- icons ----------
const svg = (html, cls) => h('span', { style: { display: 'flex' }, class: cls, html });
const icons = {
  back: '<svg width="11" height="18" viewBox="0 0 11 18" fill="none"><path d="M9.5 1.5L2 9l7.5 7.5" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  discover: '<svg width="22" height="22" viewBox="0 0 22 22" fill="none"><circle cx="11" cy="11" r="8.2" stroke="currentColor" stroke-width="1.7"/><path d="M14 8l-2 4.2L8 14l2-4.2L14 8z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/></svg>',
  plus: '<svg width="11" height="11" viewBox="0 0 11 11" fill="none"><path d="M5.5 1v9M1 5.5h9" stroke="#fff" stroke-width="1.8" stroke-linecap="round"/></svg>',
  projects: '<svg width="22" height="22" viewBox="0 0 22 22" fill="none"><rect x="3" y="6" width="12" height="13" rx="2" stroke="currentColor" stroke-width="1.7"/><path d="M7 6V5a2 2 0 012-2h8a2 2 0 012 2v10a2 2 0 01-2 2h-1" stroke="currentColor" stroke-width="1.7"/></svg>',
  profile: '<svg width="22" height="22" viewBox="0 0 22 22" fill="none"><circle cx="11" cy="7.5" r="3.6" stroke="currentColor" stroke-width="1.7"/><path d="M3.8 19c.9-3.4 3.8-5.2 7.2-5.2s6.3 1.8 7.2 5.2" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/></svg>',
  camera: '<svg width="17" height="15" viewBox="0 0 17 15" fill="none"><rect x="1" y="3.2" width="15" height="10.6" rx="2.4" stroke="currentColor" stroke-width="1.6"/><path d="M5.5 3l1.2-2h3.6L11.5 3" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/><circle cx="8.5" cy="8.4" r="2.7" stroke="currentColor" stroke-width="1.6"/></svg>',
  compare: '<svg width="13" height="13" viewBox="0 0 13 13" fill="none"><path d="M6.5 0.5v12M1 3.5h3.5v6H1zM8.5 3.5H12v6H8.5z" stroke="currentColor" stroke-width="1.3"/></svg>',
  lock: '<svg width="12" height="14" viewBox="0 0 12 14" fill="none"><rect x="1" y="6" width="10" height="7" rx="1.6" stroke="currentColor" stroke-width="1.4"/><path d="M3.2 6V4.2a2.8 2.8 0 015.6 0V6" stroke="currentColor" stroke-width="1.4"/></svg>',
  chev: '<svg width="7" height="12" viewBox="0 0 7 12" fill="none"><path d="M1 1l5 5-5 5" stroke="#B9B5AC" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>',
};

// ---------- state ----------
const S = {
  screen: localStorage.getItem('mf.onboarded') ? 'discover' : 'onboarding',
  obPage: 0,
  discover: null,
  ent: null,
  products: [],
  exhibition: null,        // slug currently viewed
  importFrom: 'discover',
  filter: 'all',
  draft: newDraft(),
  job: null,               // live job object while generating / on result
  resultProjectTitle: null,
  saved: false,
  fb: 'none',
  compareOn: false, compare: 50, hold: false,
  projects: [], projTab: 'All',
  purchases: [],
  paywall: null,           // {context}
  payPlan: 'creator_monthly',
  detail: null,            // style card for detail sheet
  infoSheet: null,         // {title, body}
  analyzing: false,
  toast: null,
  booted: false,
};
function newDraft() {
  return {
    projectId: null, assetId: null, previewUrl: null, analysis: null,
    style: null, strength: 'balanced', fidelity: 'high', composition: 'keep', ratio: '4:5',
  };
}

let toastTimer = null;
function toast(msg, ms = 1900) {
  S.toast = msg; renderOverlay();
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { S.toast = null; renderOverlay(); }, ms);
}

function go(screen) {
  S.screen = screen; S.compareOn = false; S.hold = false;
  render();
}

// ---------- data ----------
async function loadCore() {
  const [discover, ent, products] = await Promise.all([
    get('/v1/discover'), get('/v1/entitlements/me'), get('/v1/products'),
  ]);
  S.discover = discover; S.ent = ent; S.products = products.products;
}
function allShelves() {
  if (!S.discover) return [];
  return [S.discover.heroExhibition, ...S.discover.shelves];
}
function allStyles() { return allShelves().flatMap(s => s.styles); }
function findStyle(pred) { return allStyles().find(pred); }
async function refreshEnt() { S.ent = await get('/v1/entitlements/me'); }

function unitsBadgeText() {
  if (!S.ent) return '…';
  const u = S.ent.availableUnits;
  if (S.ent.plan === 'free') return u > 0 ? `${u} image${u === 1 ? '' : 's'} left` : 'No images left';
  return `${u} units`;
}

// ---------- shared components ----------
function topbar(title, onBack, opts = {}) {
  return h('div', { class: 'topbar' + (opts.clear ? ' clear' : '') },
    onBack
      ? h('button', { class: 'iconbtn', 'aria-label': 'Back', onClick: onBack }, svg(icons.back))
      : h('div', { style: { width: '38px' } }),
    h('div', { class: 'topbar-title' + (opts.serif ? ' serif' : '') }, title),
    opts.right || h('div', { style: { width: '38px' } }),
  );
}

function tabbar(active) {
  const tab = (id, icon, label, onClick) =>
    h('button', { class: 'tab' + (active === id ? ' active' : ''), onClick }, svg(icon), h('div', null, label));
  return h('div', { class: 'tabbar' },
    tab('discover', icons.discover, 'Discover', () => go('discover')),
    h('button', { class: 'tab', onClick: () => startImport(S.screen) },
      h('div', { class: 'create-dot' }, svg(icons.plus)), h('div', null, 'Create')),
    tab('projects', icons.projects, 'Projects', () => { loadProjects(); go('projects'); }),
    tab('profile', icons.profile, 'Profile', () => { loadProfile(); go('profile'); }),
  );
}

// Card artwork: real sample image when available (spec §5.1), gradient fallback.
function artBg(card) {
  return card?.coverUrl ? `url(${apiUrl(card.coverUrl)}) center/cover` : card?.coverArt;
}

function styleCardEl(card, { selected, onClick, width, showTags, reason } = {}) {
  return h('div', { style: width ? { width, flex: 'none', cursor: 'pointer' } : { cursor: 'pointer' }, onClick },
    h('div', { class: 'card-art' + (selected ? ' selected' : ''), style: { background: artBg(card) } },
      card.premium && S.ent?.plan === 'free' && h('div', { class: 'premium-badge' }, 'PREMIUM'),
      selected && h('div', { class: 'check' }, '✓'),
      h('div', { class: 'shade' }),
      h('div', { class: 'name' }, card.name),
    ),
    reason
      ? h('div', { style: { font: '500 10px/1.45 ui-monospace,Menlo,monospace', letterSpacing: '.4px', color: 'var(--cobalt)', padding: '6px 2px 0', textTransform: 'uppercase' } }, reason)
      : h('div', { class: 'card-caption' }, card.shortCaption),
    showTags && h('div', { class: 'card-tags' }, 'Best with — ' + card.suitabilityTags.join(' · ')),
  );
}

function selectStyle(card, from) {
  S.draft.style = card;
  if (from === 'discover') {
    S.exhibition = allShelves().find(s => s.styles.some(x => x.styleId === card.styleId))?.slug;
    go('exhibition');
  } else {
    render();
  }
  track('style_opened', { styleId: card.styleId, source: from });
}

// ---------- onboarding ----------
const OB = [
  { title: 'Your photos, newly seen.', body: 'One photo in, one artwork out — identity kept, direction clear.', art: 'linear-gradient(160deg,#C9BFB2 0%,#8F8275 55%,#4E463D 100%)' },
  { title: 'Choose a direction, not a prompt.', body: 'Curated exhibitions and style cards replace prompt engineering.', art: 'linear-gradient(145deg,#F1EEE4 0%,#F1EEE4 44%,#22335F 44%,#22335F 72%,#B4432E 72%,#B4432E 100%)' },
  { title: 'Private by default.', body: 'Your photos are used only to create and improve your requested result. Location data is removed.', art: 'linear-gradient(170deg,#EBECEC 0%,#C0C5C8 50%,#8F979D 100%)' },
];
function OnboardingScreen() {
  const p = OB[S.obPage];
  const finish = (dest) => {
    localStorage.setItem('mf.onboarded', '1');
    track('onboarding_completed', {});
    dest === 'import' ? startImport('discover') : go('discover');
  };
  return h('div', { class: 'screen' },
    h('div', { class: 'scroll', style: { display: 'flex', flexDirection: 'column', padding: '60px 24px 24px' } },
      h('div', { style: { font: '600 22px var(--serif)', letterSpacing: '6px', textAlign: 'center', paddingBottom: '6px' } }, 'MUSEFRAME'),
      h('div', { class: 'kicker', style: { textAlign: 'center', paddingBottom: '26px' } }, 'Curated image making'),
      h('div', { style: { borderRadius: '14px', border: '1px solid var(--line)', aspectRatio: '4/5', background: 'repeating-linear-gradient(115deg,rgba(23,23,23,.03) 0 2px,rgba(255,255,255,.02) 2px 4px),' + p.art } }),
      h('div', { style: { font: '600 26px/1.2 var(--serif)', padding: '22px 0 8px' } }, p.title),
      h('div', { style: { font: '400 14px/1.55 var(--sans)', color: 'var(--ink-muted)' } }, p.body),
      h('div', { style: { display: 'flex', gap: '6px', padding: '18px 0', justifyContent: 'center' } },
        OB.map((_, i) => h('div', { style: { width: '7px', height: '7px', borderRadius: '99px', background: i === S.obPage ? 'var(--cobalt)' : 'var(--line)' } }))),
      h('div', { style: { flex: 1 } }),
      S.obPage < OB.length - 1
        ? h('button', { class: 'btn', onClick: () => { S.obPage++; render(); } }, 'Continue')
        : h('button', { class: 'btn', onClick: () => finish('import') }, 'Choose a photo'),
      h('button', { class: 'linkbtn', style: { padding: '14px 0 4px', textAlign: 'center', width: '100%' }, onClick: () => finish('discover') }, 'Explore first'),
    ),
  );
}

// ---------- discover ----------
function DiscoverScreen() {
  const d = S.discover;
  if (!d) {
    // No dead spinners (spec §6.1): connection failure gets a clear message + retry.
    if (S.bootError) return h('div', { class: 'screen' },
      h('div', { style: { margin: 'auto', textAlign: 'center', padding: '0 40px' } },
        h('div', { style: { font: '600 22px var(--serif)', paddingBottom: '8px' } }, 'Can’t reach the gallery'),
        h('div', { style: { font: '400 13px/1.6 var(--sans)', color: 'var(--ink-muted)', paddingBottom: '20px' } },
          'Check your connection and try again. Your drafts and works are safe.'),
        h('button', { class: 'btn', style: { width: 'auto', padding: '0 28px', margin: '0 auto' }, onClick: bootApp }, 'Try again')),
      tabbar('discover'));
    return h('div', { class: 'screen' }, h('div', { style: { margin: 'auto' } }, h('div', { class: 'spinner' })));
  }
  const hero = d.heroExhibition;
  return h('div', { class: 'screen' },
    h('div', { class: 'scroll', style: { paddingBottom: '110px' } },
      h('div', { style: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: 'max(22px, env(safe-area-inset-top)) 20px 2px' } },
        h('div', { class: 'kicker' }, `${hero.edition} · AUG 2026`),
        h('button', { class: 'pillbtn', onClick: () => openPaywall('badge') }, unitsBadgeText()),
      ),
      h('div', { class: 'page-title', style: { padding: '4px 20px 16px' } }, 'Discover'),
      // hero exhibition
      h('div', { style: { padding: '0 20px' } },
        h('div', { style: { position: 'relative', borderRadius: '14px', overflow: 'hidden', border: '1px solid var(--line)' } },
          h('div', { style: { aspectRatio: '4/5', background: artBg(hero.styles[0]) } }),
          h('div', { style: { position: 'absolute', inset: 0, background: 'linear-gradient(180deg,rgba(0,0,0,0) 40%,rgba(0,0,0,.6) 100%)' } }),
          h('div', { style: { position: 'absolute', left: '18px', right: '18px', bottom: '18px', display: 'flex', flexDirection: 'column', gap: '8px', alignItems: 'flex-start' } },
            h('div', { style: { font: '500 10px var(--mono)', letterSpacing: '1.8px', color: 'rgba(247,245,239,.85)' } }, `EXHIBITION · ${hero.styles.length} STYLES`),
            h('div', { style: { font: '600 30px/1.1 var(--serif)', color: '#fff' } }, hero.title),
            h('div', { style: { font: '400 13px/1.5 var(--sans)', color: 'rgba(247,245,239,.92)' } }, hero.curatorialNote),
            h('button', {
              style: { marginTop: '4px', background: '#fff', color: 'var(--ink)', font: '600 13px var(--sans)', padding: '10px 18px', borderRadius: '999px', border: 'none', cursor: 'pointer' },
              onClick: () => { S.exhibition = hero.slug; track('exhibition_opened', { slug: hero.slug }); go('exhibition'); },
            }, 'View exhibition'),
          ),
        ),
      ),
      // for-your-photo card
      h('div', { style: { margin: '14px 20px 0', display: 'flex', alignItems: 'center', gap: '12px', background: 'var(--surface)', border: '1px solid var(--line)', borderRadius: '14px', padding: '12px 14px' } },
        h('div', { style: { width: '46px', height: '46px', borderRadius: '10px', flex: 'none', background: S.draft.previewUrl ? `url(${S.draft.previewUrl}) center/cover` : 'linear-gradient(170deg,#B9AE9A 0%,#8E8878 45%,#544F45 100%)', filter: S.draft.previewUrl ? 'none' : 'blur(1px)' } }),
        h('div', { style: { flex: 1, minWidth: 0 } },
          h('div', { style: { font: '600 13.5px var(--sans)' } }, 'For your photo'),
          h('div', { style: { font: '400 11.5px/1.4 var(--sans)', color: 'var(--ink-muted)' } }, 'Directions matched to your shot.'),
        ),
        h('button', { class: 'linkbtn', style: { flex: 'none' }, onClick: () => startImport('discover') }, 'Find a direction'),
      ),
      // shelves
      d.shelves.map(sh => h('div', { style: { marginTop: '30px' } },
        h('div', { style: { display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', padding: '0 20px 10px' } },
          h('div', { style: { font: '600 20px var(--serif)' } }, sh.title),
          h('button', { class: 'linkbtn', style: { fontWeight: 500, fontSize: '12px' }, onClick: () => { S.exhibition = sh.slug; go('exhibition'); } }, 'View all'),
        ),
        h('div', { class: 'hrow' },
          sh.styles.map(c => styleCardEl(c, { width: '146px', onClick: () => selectStyle(c, 'discover') }))),
      )),
      h('div', { style: { textAlign: 'center', font: '400 11px var(--sans)', color: 'var(--ink-muted)', padding: '34px 20px 8px' } },
        h('button', { class: 'linkbtn', style: { color: 'var(--ink-muted)', fontWeight: 400, fontSize: '11px' }, onClick: () => openInfo('about') }, 'About our styles'), ' · ',
        h('button', { class: 'linkbtn', style: { color: 'var(--ink-muted)', fontWeight: 400, fontSize: '11px' }, onClick: () => openInfo('privacy') }, 'Privacy'),
      ),
    ),
    tabbar('discover'),
  );
}

// ---------- exhibition ----------
function ExhibitionScreen() {
  const exh = allShelves().find(s => s.slug === S.exhibition) || S.discover.heroExhibition;
  return h('div', { class: 'screen' },
    topbar('Exhibition', () => go('discover')),
    h('div', { class: 'scroll', style: { padding: '14px 20px 120px' } },
      h('div', { style: { position: 'relative', borderRadius: '14px', overflow: 'hidden', border: '1px solid var(--line)', aspectRatio: '16/10', background: artBg(exh.styles[0]) } },
        h('div', { style: { position: 'absolute', inset: 0, background: 'linear-gradient(180deg,rgba(0,0,0,0) 45%,rgba(0,0,0,.45) 100%)' } }),
        h('div', { style: { position: 'absolute', left: '14px', bottom: '12px', font: '500 9.5px var(--mono)', letterSpacing: '1.6px', color: 'rgba(247,245,239,.85)' } }, `${exh.edition} · ${exh.title.toUpperCase()}`),
      ),
      h('div', { style: { font: '600 28px/1.15 var(--serif)', padding: '16px 0 6px' } }, exh.title),
      h('div', { style: { font: '400 13.5px/1.55 var(--sans)', color: 'var(--ink-muted)' } }, exh.curatorialNote),
      h('div', { class: 'grid2', style: { paddingTop: '18px' } },
        exh.styles.map(c => styleCardEl(c, {
          selected: S.draft.style?.styleId === c.styleId,
          showTags: true,
          onClick: () => { S.draft.style = c; S.detail = c; render(); },
        }))),
    ),
    h('div', { class: 'bottom-bar' },
      h('button', { class: 'btn', onClick: () => startImport('exhibition') }, 'Choose a photo')),
  );
}

// ---------- import ----------
function startImport(from) {
  S.importFrom = from;
  go('import');
  track('photo_import_started', { source: from });
}

function ImportScreen() {
  const fileInput = h('input', { type: 'file', accept: 'image/*', style: { display: 'none' }, onChange: (e) => e.target.files[0] && handleFile(e.target.files[0]) });
  const cameraInput = h('input', { type: 'file', accept: 'image/*', capture: 'environment', style: { display: 'none' }, onChange: (e) => e.target.files[0] && handleFile(e.target.files[0]) });
  return h('div', { class: 'screen' },
    topbar('Choose a photo', () => go(S.importFrom === 'exhibition' ? 'exhibition' : 'discover')),
    h('div', { class: 'scroll', style: { padding: '18px 20px 40px' } },
      fileInput, cameraInput,
      h('div', { class: 'kicker', style: { paddingBottom: '10px' } }, 'Photo library'),
      h('div', {
        style: { border: '1px dashed var(--line)', borderRadius: '14px', background: 'var(--surface)', padding: '38px 20px', textAlign: 'center', cursor: 'pointer' },
        onClick: () => fileInput.click(),
      },
        h('div', { style: { font: '600 17px var(--serif)' } }, 'Choose from your photos'),
        h('div', { style: { font: '400 12.5px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '6px' } }, 'JPEG, PNG or HEIC · up to 20 MB'),
        h('div', { style: { paddingTop: '14px' } }, h('span', { class: 'chip active' }, 'Browse')),
      ),
      h('div', { style: { display: 'flex', alignItems: 'center', gap: '12px', padding: '22px 0 14px' } },
        h('div', { style: { flex: 1, height: '1px', background: 'var(--line)' } }),
        h('div', { style: { font: '400 11px var(--sans)', color: 'var(--ink-muted)' } }, 'or'),
        h('div', { style: { flex: 1, height: '1px', background: 'var(--line)' } })),
      h('button', { class: 'btn secondary', onClick: () => cameraInput.click() }, svg(icons.camera), 'Take a photo'),
      h('div', { style: { display: 'flex', alignItems: 'flex-start', gap: '8px', paddingTop: '20px', color: 'var(--ink-muted)' } },
        svg(icons.lock, ''),
        h('div', { style: { font: '400 11px/1.55 var(--sans)' } }, 'Location data is removed. Your photo is used only to create the result you request, then follows your retention setting.')),
    ),
  );
}

// Downscale + re-encode to JPEG client-side (strips EXIF/GPS by re-encoding).
// Falls back to <img> decoding on WebViews without createImageBitmap(File).
async function decodeToSource(file) {
  const bmp = await createImageBitmap(file).catch(() => null);
  if (bmp) return bmp;
  return await new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => { URL.revokeObjectURL(url); reject(new Error('decode')); };
    img.src = url;
  });
}
async function toJpeg(file, maxEdge = 2048) {
  const src = await decodeToSource(file);
  const sw = src.naturalWidth || src.width, sh = src.naturalHeight || src.height;
  const scale = Math.min(1, maxEdge / Math.max(sw, sh));
  const w = Math.round(sw * scale), hgt = Math.round(sh * scale);
  const canvas = document.createElement('canvas');
  canvas.width = w; canvas.height = hgt;
  canvas.getContext('2d').drawImage(src, 0, 0, w, hgt);
  const blob = await new Promise(r => canvas.toBlob(r, 'image/jpeg', 0.92));
  if (!blob) throw new Error('decode');
  return { blob, width: w, height: hgt };
}

async function handleFile(file) {
  S.analyzing = true; renderOverlay();
  try {
    if (file.size > 20 * 1024 * 1024) { toast('Images up to 20 MB are supported'); S.analyzing = false; renderOverlay(); return; }
    const { blob, width, height } = await toJpeg(file);
    if (Math.min(width, height) < 320) { toast('This photo is too small to style well'); S.analyzing = false; renderOverlay(); return; }
    const intent = await post('/v1/assets/upload-intents', { contentType: 'image/jpeg', byteSize: blob.size });
    await put(intent.uploadUrl, blob);
    await post(`/v1/assets/${intent.assetId}/complete`);
    const project = await post('/v1/projects', { sourceAssetId: intent.assetId });

    const keepStyle = S.importFrom === 'exhibition' ? S.draft.style : null;
    S.draft = newDraft();
    S.draft.projectId = project.id;
    S.draft.assetId = intent.assetId;
    S.draft.previewUrl = URL.createObjectURL(blob);
    S.draft.style = keepStyle;
    if (Math.min(width, height) < 768) toast('Low resolution — quality may be reduced', 2500);
    track('photo_import_completed', { megapixelBucket: Math.round(width * height / 1e6) });

    // Poll analysis briefly; don't block browsing styles (spec §6.7).
    const deadline = Date.now() + 4000;
    while (Date.now() < deadline) {
      const a = await get(`/v1/assets/${intent.assetId}/analysis`).catch(() => null);
      if (a && a.status !== 'pending') { S.draft.analysis = a; break; }
      await new Promise(r => setTimeout(r, 450));
    }
    S.analyzing = false;
    go(keepStyle ? 'configure' : 'styles');
    renderOverlay();
    track('analysis_completed', { subjectType: S.draft.analysis?.subjectType });
  } catch (e) {
    S.analyzing = false; renderOverlay();
    // Distinguish decode problems from connectivity problems (spec §23 recoverable errors).
    if (e.code === 'ASSET_UNSUPPORTED') toast(e.message);
    else if (e.message === 'decode' || e.message === 'unsupported') toast('Could not read that image — try another');
    else toast('Connection problem — check your network and try again', 2600);
  }
}

// ---------- styles ----------
const FILTERS = [['all', 'All'], ['portrait', 'Portrait'], ['landscape', 'Landscape'], ['object', 'Object'], ['pet', 'Pet']];
function StylesScreen() {
  const a = S.draft.analysis;
  const recs = (a?.recommendations || [])
    .map(r => ({ card: findStyle(c => c.styleVersionId === r.styleVersionId), reason: r.reasonCode.replaceAll('_', ' ') }))
    .filter(r => r.card);
  const subjectLabel = a?.status === 'ready'
    ? `${a.subjectType}${a.subjectType === 'person' ? `, ${a.personCount} person` : ''}`
    : 'reading…';
  const match = (c) => S.filter === 'all' || c.suitabilityTags.includes(S.filter);
  return h('div', { class: 'screen' },
    topbar('Choose a direction', () => go('import')),
    h('div', { class: 'scroll', style: { paddingBottom: '130px' } },
      // photo chip
      h('div', { style: { display: 'flex', alignItems: 'center', gap: '10px', margin: '14px 20px 0', background: 'var(--surface)', border: '1px solid var(--line)', borderRadius: '12px', padding: '9px 12px' } },
        h('div', { style: { width: '34px', height: '34px', borderRadius: '8px', flex: 'none', background: `url(${S.draft.previewUrl}) center/cover` } }),
        h('div', { style: { flex: 1, font: '500 12.5px var(--sans)' } }, 'For this photo ', h('span', { style: { color: 'var(--ink-muted)', fontWeight: 400 } }, `· ${subjectLabel}`)),
        h('button', { class: 'linkbtn', onClick: () => go('import') }, 'Change'),
      ),
      // filters
      h('div', { style: { display: 'flex', gap: '8px', padding: '14px 20px 0', overflowX: 'auto' } },
        FILTERS.map(([key, label]) => h('button', {
          class: 'chip' + (S.filter === key ? ' active' : ''),
          onClick: () => { S.filter = key; render(); },
        }, label))),
      // recommended
      recs.length > 0 && [
        h('div', { class: 'kicker', style: { padding: '22px 20px 10px' } }, 'Recommended'),
        h('div', { class: 'hrow' },
          recs.map(({ card, reason }) => styleCardEl(card, {
            width: '138px', reason,
            selected: S.draft.style?.styleId === card.styleId,
            onClick: () => { S.draft.style = card; render(); },
          }))),
      ],
      // groups
      allShelves().map(sh => {
        const cards = sh.styles.filter(match);
        if (!cards.length) return null;
        return h('div', { style: { paddingTop: '26px' } },
          h('div', { style: { font: '600 19px var(--serif)', padding: '0 20px 2px' } }, sh.title),
          h('div', { style: { font: '400 12px/1.5 var(--sans)', color: 'var(--ink-muted)', padding: '0 20px 12px' } }, sh.curatorialNote),
          h('div', { class: 'grid2', style: { padding: '0 20px' } },
            cards.map(c => styleCardEl(c, {
              selected: S.draft.style?.styleId === c.styleId,
              onClick: () => { S.draft.style = c; render(); },
            }))),
        );
      }),
    ),
    S.draft.style && h('div', { class: 'bottom-bar', style: { display: 'flex', alignItems: 'center', gap: '12px', background: 'rgba(255,255,255,.94)' } },
      h('div', { style: { flex: 1, minWidth: 0 } },
        h('div', { class: 'kicker', style: { fontSize: '10px', letterSpacing: '1.4px' } }, 'Selected'),
        h('div', { style: { font: '600 14px var(--serif)' } }, S.draft.style.name)),
      h('button', { class: 'linkbtn', onClick: () => { S.detail = S.draft.style; renderOverlay(); } }, 'Details'),
      h('button', { class: 'btn', style: { width: 'auto', height: '46px', padding: '0 20px', fontSize: '14.5px' }, onClick: () => go('configure') }, 'Preview settings'),
    ),
  );
}

// ---------- configure ----------
function ConfigureScreen() {
  const d = S.draft, card = d.style;
  const seg = (label, key, options, labels) => h('div', { style: { paddingTop: '20px' } },
    h('div', { style: { font: '600 13px var(--sans)', paddingBottom: '8px' } }, label),
    h('div', { class: 'seg' },
      options.map((v, i) => h('button', {
        class: d[key] === v ? 'active' : '',
        onClick: () => { d[key] = v; render(); },
      }, labels ? labels[i] : v[0].toUpperCase() + v.slice(1)))),
  );
  const premiumNote = card.premium && S.ent?.plan === 'free';
  return h('div', { class: 'screen' },
    topbar('Preview settings', () => go('styles')),
    h('div', { class: 'scroll', style: { padding: '18px 20px 120px' } },
      h('div', { class: 'panel', style: { display: 'flex', alignItems: 'center', gap: '14px', padding: '14px' } },
        h('div', { style: { display: 'flex', flexDirection: 'column', gap: '6px', alignItems: 'center' } },
          h('div', { style: { width: '64px', height: '80px', borderRadius: '10px', background: `url(${d.previewUrl}) center/cover` } }),
          h('div', { style: { font: '500 9px var(--mono)', letterSpacing: '1px', color: 'var(--ink-muted)' } }, 'YOUR PHOTO')),
        h('div', { html: '<svg width="18" height="12" viewBox="0 0 18 12" fill="none"><path d="M1 6h15M12 1.5L16.5 6 12 10.5" stroke="#6E6B66" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>', style: { flex: 'none', display: 'flex' } }),
        h('div', { style: { display: 'flex', flexDirection: 'column', gap: '6px', alignItems: 'center' } },
          h('div', { style: { width: '64px', height: '80px', borderRadius: '10px', background: artBg(card), backgroundSize: 'cover' } }),
          h('div', { style: { font: '500 9px var(--mono)', letterSpacing: '1px', color: 'var(--ink-muted)' } }, 'DIRECTION')),
        h('div', { style: { flex: 1, minWidth: 0, paddingLeft: '2px' } },
          h('div', { style: { font: '600 16px var(--serif)' } }, card.name),
          h('div', { style: { font: '400 11.5px/1.45 var(--sans)', color: 'var(--ink-muted)', paddingTop: '2px' } }, card.shortCaption)),
      ),
      premiumNote && h('div', { style: { marginTop: '12px', font: '500 11.5px/1.5 var(--sans)', color: 'var(--warning)', background: 'var(--warning-soft)', borderRadius: '10px', padding: '9px 12px' } },
        'Premium direction — included with Creator. You can preview settings freely.'),
      seg('Style strength', 'strength', ['soft', 'balanced', 'bold']),
      seg('Subject fidelity', 'fidelity', ['high', 'natural']),
      seg('Composition', 'composition', ['keep', 'reframe']),
      seg('Output ratio', 'ratio', ['original', '1:1', '4:5', '16:9'], ['Original', '1:1', '4:5', '16:9']),
      h('div', { style: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginTop: '24px', borderTop: '1px solid var(--line)', paddingTop: '14px' } },
        h('div', { class: 'kicker', style: { letterSpacing: '1.2px' } }, estimateLabel()),
        h('div', { style: { font: '500 11px var(--sans)', color: 'var(--ink-muted)' } }, unitsBadgeText())),
    ),
    h('div', { class: 'bottom-bar' },
      h('button', { class: 'btn', onClick: generate }, 'Generate')),
  );
}

// ---------- generation ----------
function estimateLabel() {
  const r = S.lastEstimate || S.discover?.generation?.estimatedRangeSeconds;
  if (r && r[1] > 120) return `Estimate — 1 standard image · ${Math.round(r[0] / 60)}–${Math.round(r[1] / 60)} min`;
  return 'Estimate — 1 standard image · 20–90 s';
}
let pollTimer = null;
async function generate() {
  const d = S.draft;
  if (!d.projectId || !d.assetId) { startImport(S.screen); return; }
  try {
    track('generation_submitted', { styleId: d.style.styleId });
    const res = await post('/v1/generation-jobs', {
      projectId: d.projectId,
      sourceAssetId: d.assetId,
      styleVersionId: d.style.styleVersionId,
      controls: { strength: d.strength, fidelity: d.fidelity, composition: d.composition },
      output: { aspectRatio: d.ratio, qualityTier: 'standard' },
      parentJobId: S.job?.id || null,
    }, { 'Idempotency-Key': crypto.randomUUID() });
    S.job = res.job;
    S.lastEstimate = res.job.estimatedRangeSeconds;
    S.jobStartedAt = Date.now();
    S.saved = false; S.fb = 'none'; S.compareOn = false; S.compare = 50;
    await refreshEnt();
    go('progress');
    pollJob(res.job.id);
  } catch (e) {
    if (e.code === 'INSUFFICIENT_ENTITLEMENT') openPaywall(e.details?.premiumStyle ? 'premium' : 'units');
    else toast(e.message || 'Could not start — try again');
  }
}

function pollJob(jobId) {
  clearInterval(pollTimer);
  pollTimer = setInterval(async () => {
    try {
      const job = await get(`/v1/generation-jobs/${jobId}`);
      S.job = job;
      if (job.status === 'succeeded') {
        clearInterval(pollTimer);
        await refreshEnt();
        track('generation_succeeded', { jobId });
        if (S.screen === 'progress') go('result');
        else { toast('Your image is ready — see Projects'); render(); }
      } else if (job.status === 'failed' || job.status === 'cancelled') {
        clearInterval(pollTimer);
        await refreshEnt();
        track('generation_failed', { jobId, errorCode: job.error?.code });
        if (S.screen === 'progress') {
          toast(job.error?.code === 'GENERATION_REJECTED' ? 'This request can’t be created — no units used' : 'Something went wrong — no units used', 2600);
          go('configure');
        }
      } else if (S.screen === 'progress') render();
    } catch { /* transient poll error — keep trying */ }
  }, 2000);
}

const STAGES = [
  ['preparing', 'Preparing your photo'],
  ['building', 'Building the direction'],
  ['making', 'Making the image'],
  ['checking', 'Checking the result'],
];
function ProgressScreen() {
  const stageIdx = Math.max(0, STAGES.findIndex(([k]) => k === S.job?.stage));
  return h('div', { class: 'screen', style: { overflow: 'hidden' } },
    h('div', { style: { position: 'absolute', inset: '-30px', background: S.draft.previewUrl ? `url(${S.draft.previewUrl}) center/cover` : 'var(--canvas)', filter: 'blur(30px) saturate(.65) brightness(.95)' } }),
    h('div', { style: { position: 'absolute', inset: 0, background: 'rgba(247,245,239,.78)' } }),
    h('div', { style: { position: 'absolute', inset: 0, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '0 36px' } },
      h('div', { class: 'spinner', style: { width: '34px', height: '34px' } }),
      h('div', { style: { font: '600 24px var(--serif)', padding: '18px 0 4px', textAlign: 'center' } }, STAGES[stageIdx][1]),
      h('div', { class: 'kicker', style: { paddingBottom: '26px' } }, `Step ${stageIdx + 1} of 4`),
      h('div', { style: { display: 'flex', flexDirection: 'column', gap: '11px', alignSelf: 'stretch', background: 'rgba(255,255,255,.75)', border: '1px solid rgba(217,213,204,.8)', borderRadius: '14px', padding: '16px 18px' } },
        STAGES.map(([, label], i) => h('div', { style: { display: 'flex', alignItems: 'center', gap: '10px' } },
          h('div', { style: { width: '8px', height: '8px', borderRadius: '999px', flex: 'none', background: i < stageIdx ? 'var(--success)' : i === stageIdx ? 'var(--cobalt)' : 'var(--line)', animation: i === stageIdx ? 'mfPulse 1.1s ease infinite' : 'none' } }),
          h('div', { style: { font: '500 13px var(--sans)', color: i <= stageIdx ? 'var(--ink)' : 'var(--ink-muted)' } }, label)))),
      h('div', { style: { font: '400 12px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '22px', textAlign: 'center' } },
        S.jobStartedAt && S.lastEstimate && (Date.now() - S.jobStartedAt) / 1000 > S.lastEstimate[1]
          ? 'Still working — this one is taking longer than usual.'
          : 'You can leave — creation continues in the background.'),
      h('button', { class: 'linkbtn', style: { paddingTop: '10px', fontSize: '13px' }, onClick: () => go('discover') }, 'Back to Discover'),
    ),
  );
}

// ---------- result ----------
function ResultScreen() {
  const job = S.job;
  if (!job?.candidate) return ProgressScreen();
  const cand = job.candidate;
  const resultUrl = assetUrl(cand.assetId);
  const sourceUrl = S.draft.previewUrl || (S.draft.assetId ? assetUrl(S.draft.assetId) : null);
  const styleName = S.draft.style?.name || 'Result';

  const artBox = h('div', {
    style: {
      position: 'relative', borderRadius: '14px', overflow: 'hidden', border: '1px solid var(--line)',
      aspectRatio: `${cand.width}/${cand.height}`, cursor: 'pointer', userSelect: 'none', touchAction: 'none',
      maxHeight: '58vh', margin: '0 auto',
    },
    onPointerDown: (e) => { if (!S.compareOn) { e.preventDefault(); S.hold = true; render(); } },
    onPointerUp: () => { if (S.hold) { S.hold = false; render(); } },
    onPointerLeave: () => { if (S.hold) { S.hold = false; render(); } },
  },
    sourceUrl && h('img', { src: sourceUrl, style: { position: 'absolute', inset: 0, width: '100%', height: '100%', objectFit: 'cover' }, alt: 'Original photo' }),
    h('img', {
      src: resultUrl, alt: `Styled result — ${styleName}`,
      style: {
        position: 'absolute', inset: 0, width: '100%', height: '100%', objectFit: 'cover',
        opacity: S.hold ? 0 : 1, transition: 'opacity .16s',
        clipPath: S.compareOn ? `inset(0 0 0 ${S.compare}%)` : 'none',
      },
    }),
    S.compareOn && [
      h('div', { style: { position: 'absolute', top: 0, bottom: 0, left: `${S.compare}%`, width: '2px', background: 'rgba(255,255,255,.95)', boxShadow: '0 0 10px rgba(0,0,0,.4)' } }),
      h('div', { style: { position: 'absolute', top: '10px', left: '10px', font: '500 10px var(--sans)', color: '#fff', background: 'var(--overlay)', borderRadius: '6px', padding: '3px 8px' } }, 'Before'),
      h('div', { style: { position: 'absolute', top: '10px', right: '10px', font: '500 10px var(--sans)', color: '#fff', background: 'var(--overlay)', borderRadius: '6px', padding: '3px 8px' } }, 'After'),
    ],
    !S.compareOn && !S.hold && h('div', { style: { position: 'absolute', bottom: '10px', left: '50%', transform: 'translateX(-50%)', font: '500 10.5px var(--sans)', color: '#fff', background: 'var(--overlay)', borderRadius: '999px', padding: '5px 12px', whiteSpace: 'nowrap' } }, 'Hold to see original'),
  );

  return h('div', { class: 'screen' },
    topbar(styleName, () => go('discover'), { serif: true }),
    h('div', { class: 'scroll', style: { padding: '6px 16px 40px' } },
      artBox,
      h('div', { style: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '12px 2px 0' } },
        h('button', {
          class: 'chip', style: S.compareOn ? { background: 'var(--ink)', color: '#fff', borderColor: 'var(--ink)', display: 'flex', gap: '7px', alignItems: 'center' } : { display: 'flex', gap: '7px', alignItems: 'center' },
          onClick: () => { S.compareOn = !S.compareOn; S.hold = false; render(); },
        }, svg(icons.compare), 'Compare'),
        h('div', { style: { font: '500 9.5px var(--mono)', letterSpacing: '1px', color: 'var(--ink-muted)' } }, `${cand.width} × ${cand.height} · STANDARD · PRIVATE`)),
      S.compareOn && h('input', {
        type: 'range', min: 0, max: 100, value: S.compare, style: { marginTop: '12px' },
        onInput: (e) => { S.compare = +e.target.value; render(); },
      }),
      h('button', { class: 'btn' + (S.saved ? ' success' : ''), style: { marginTop: '14px' }, onClick: saveResult }, S.saved ? 'Saved ✓' : 'Save'),
      h('div', { style: { display: 'flex', gap: '10px', paddingTop: '10px' } },
        h('button', { class: 'btn secondary small', style: { flex: 1 }, onClick: shareResult }, 'Share'),
        h('button', { class: 'btn secondary small', style: { flex: 1 }, onClick: generate }, 'Try again'),
        h('button', { class: 'btn secondary small', style: { flex: 1 }, onClick: () => go('configure') }, 'Refine')),
      FeedbackRow(cand.id),
    ),
  );
}

function FeedbackRow(candidateId) {
  if (S.fb === 'done') return h('div', { style: { textAlign: 'center', font: '400 12px var(--sans)', color: 'var(--success)', paddingTop: '20px' } }, 'Thanks — noted for this direction.');
  if (S.fb === 'pick') {
    const reasons = [['FACE_CHANGED', 'Face changed'], ['WRONG_STYLE', 'Wrong style'], ['BAD_DETAILS', 'Bad details'], ['TOO_STRONG', 'Too strong']];
    return h('div', { style: { display: 'flex', flexWrap: 'wrap', justifyContent: 'center', gap: '8px', paddingTop: '18px' } },
      reasons.map(([code, label]) => h('button', {
        class: 'chip', style: { fontWeight: 500, fontSize: '11.5px' },
        onClick: () => { post(`/v1/candidates/${candidateId}/feedback`, { rating: 'negative', reasonCodes: [code] }).catch(() => {}); S.fb = 'done'; render(); },
      }, label)));
  }
  return h('div', { style: { display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '10px', paddingTop: '20px' } },
    h('div', { style: { font: '400 12px var(--sans)', color: 'var(--ink-muted)' } }, 'How did it come out?'),
    h('button', { class: 'chip', onClick: () => { post(`/v1/candidates/${S.job.candidate.id}/feedback`, { rating: 'positive' }).catch(() => {}); S.fb = 'done'; render(); } }, 'Love it'),
    h('button', { class: 'chip', onClick: () => { S.fb = 'pick'; render(); } }, 'Not quite'));
}

async function saveResult() {
  if (S.saved) return;
  try {
    const exp = await post(`/v1/candidates/${S.job.candidate.id}/export`, { qualityTier: 'standard', format: 'jpeg' });
    const blob = await fetch(apiUrl(exp.downloadUrl) + `?token=${encodeURIComponent(localStorage.getItem('mf.session'))}`).then(r => r.blob());
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `museframe-${(S.draft.style?.name || 'work').toLowerCase().replaceAll(' ', '-')}.jpg`;
    a.click();
    S.saved = true; render();
    toast('Saved');
    if (S.ent?.plan === 'free') setTimeout(() => openPaywall('post_save'), 1800);
  } catch (e) { toast('Could not save — try again'); }
}

async function shareResult() {
  const url = assetUrl(S.job.candidate.assetId);
  try {
    const blob = await fetch(url).then(r => r.blob());
    const file = new File([blob], 'museframe.jpg', { type: 'image/jpeg' });
    if (navigator.canShare?.({ files: [file] })) {
      await navigator.share({ files: [file], title: 'MuseFrame' });
      track('result_shared', {});
      return;
    }
  } catch { /* fall through */ }
  toast('Sharing not available here — use Save');
}

// ---------- projects ----------
async function loadProjects() {
  try { S.projects = (await get('/v1/projects')).projects; render(); } catch { }
}
const STATUS_LABEL = { draft: 'Draft', generating: 'In progress', ready: 'New', saved: 'Saved' };
const STATUS_COLOR = { draft: ['#6E6B66', '#EFEDE6'], generating: ['#A36513', '#F6EBD9'], ready: ['#1C49D8', '#E8EDFF'], saved: ['#217A54', '#E3F0E9'] };
function ProjectsScreen() {
  const tabs = ['All', 'In progress', 'Saved'];
  const filtered = S.projects.filter(p => {
    if (S.projTab === 'In progress') return p.status === 'generating';
    if (S.projTab === 'Saved') return p.status === 'saved';
    return true;
  });
  return h('div', { class: 'screen' },
    h('div', { class: 'scroll', style: { padding: 'max(22px, env(safe-area-inset-top)) 20px 116px' } },
      h('div', { class: 'page-title', style: { padding: '14px 0' } }, 'Projects'),
      h('div', { style: { display: 'flex', gap: '8px', paddingBottom: '16px' } },
        tabs.map(t => h('button', { class: 'chip' + (S.projTab === t ? ' active' : ''), onClick: () => { S.projTab = t; render(); } }, t))),
      filtered.length === 0
        ? h('div', { style: { textAlign: 'center', padding: '60px 20px', border: '1px dashed var(--line)', borderRadius: '14px' } },
          h('div', { style: { font: '600 17px var(--serif)' } }, 'Nothing here yet'),
          h('div', { style: { font: '400 12.5px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '6px' } }, 'Works you create will appear in this room.'))
        : h('div', { class: 'grid2', style: { gap: '14px' } },
          filtered.map(p => {
            const [fg, bg] = STATUS_COLOR[p.status] || STATUS_COLOR.ready;
            const imgId = p.candidateAssetId || p.sourceAssetId;
            return h('div', { style: { cursor: 'pointer' }, onClick: () => openProject(p) },
              h('div', { class: 'card-art' },
                imgId && h('img', { src: assetUrl(imgId), style: { position: 'absolute', inset: 0, width: '100%', height: '100%', objectFit: 'cover' }, loading: 'lazy', alt: p.styleName || 'Project' }),
                h('div', { style: { position: 'absolute', top: '8px', left: '8px', font: '600 9.5px var(--sans)', letterSpacing: '.4px', color: fg, background: bg, borderRadius: '999px', padding: '3px 8px' } }, STATUS_LABEL[p.status] || p.status)),
              h('div', { style: { font: '600 13px var(--sans)', padding: '7px 2px 0' } }, p.styleName || p.title || 'Untitled'),
              h('div', { style: { font: '400 11px var(--sans)', color: 'var(--ink-muted)', padding: '2px 2px 0' } }, new Date(p.updatedAt).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })),
            );
          })),
    ),
    tabbar('projects'),
  );
}

async function openProject(p) {
  if (!p.jobId) { toast('This project has no work yet'); return; }
  try {
    // Prefer a live attempt, else the newest succeeded one — a failed retry
    // must never hide an earlier finished work (spec §6.13).
    const detail = await get(`/v1/projects/${p.id}`);
    const active = detail.jobs.find(j => ['queued', 'running', 'quality_check', 'created'].includes(j.status));
    const done = detail.jobs.find(j => j.status === 'succeeded');
    const jobId = active?.id || done?.id || p.jobId;
    const job = await get(`/v1/generation-jobs/${jobId}`);
    S.job = job;
    S.draft.projectId = p.id;
    S.draft.assetId = p.sourceAssetId;
    S.draft.previewUrl = p.sourceAssetId ? assetUrl(p.sourceAssetId) : null;
    S.draft.style = findStyle(c => c.styleVersionId === job.styleVersionId) || S.draft.style;
    if (job.controls) Object.assign(S.draft, job.controls, { ratio: job.output?.aspectRatio || '4:5' });
    S.saved = p.status === 'saved'; S.fb = 'none';
    if (job.status === 'succeeded') go('result');
    else if (['queued', 'running', 'quality_check', 'created'].includes(job.status)) { go('progress'); pollJob(job.id); }
    else toast('That attempt failed — try a new one');
  } catch { toast('Could not open project'); }
}

// ---------- profile ----------
async function loadProfile() {
  try {
    const [ent, purchases] = await Promise.all([get('/v1/entitlements/me'), get('/v1/purchases')]);
    S.ent = ent; S.purchases = purchases.purchases; render();
  } catch { }
}
function ProfileScreen() {
  const ent = S.ent || { plan: 'free', availableUnits: 0 };
  const isFree = ent.plan === 'free';
  const planName = isFree ? 'Free plan' : (S.products.find(p => p.internalKey === ent.plan)?.displayName || 'Creator');
  const rows = [
    ['Manage subscription', () => openPaywall('manage')],
    ['Restore purchases', async () => { await refreshEnt(); render(); toast(S.ent.plan === 'free' ? 'No active plan found' : 'Purchases restored'); }],
    ['Purchase history', () => openInfo('purchases')],
    ['Privacy & data', () => openInfo('privacy')],
    ['About our styles', () => openInfo('about')],
    ['Delete account', () => openInfo('delete')],
  ];
  return h('div', { class: 'screen' },
    h('div', { class: 'scroll', style: { padding: 'max(22px, env(safe-area-inset-top)) 20px 116px' } },
      h('div', { class: 'page-title', style: { padding: '14px 0 16px' } }, 'Profile'),
      h('div', { style: { display: 'flex', alignItems: 'center', gap: '14px', paddingBottom: '18px' } },
        h('div', { style: { width: '54px', height: '54px', borderRadius: '999px', background: 'var(--ink)', color: 'var(--canvas)', display: 'flex', alignItems: 'center', justifyContent: 'center', font: '600 22px var(--serif)' } }, 'G'),
        h('div', null,
          h('div', { style: { font: '600 16px var(--sans)' } }, 'Guest'),
          h('div', { style: { font: '400 12px var(--sans)', color: 'var(--ink-muted)' } }, 'Works are saved on this device'))),
      h('div', { class: 'panel', style: { padding: '16px' } },
        h('div', { style: { display: 'flex', alignItems: 'center', justifyContent: 'space-between' } },
          h('div', { style: { font: '600 15px var(--serif)' } }, planName),
          h('span', { class: 'pillbtn', style: { cursor: 'default' } }, unitsBadgeText())),
        h('div', { style: { font: '400 12px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '4px' } },
          isFree ? 'First artwork included. Subscribe for more directions and priority creation.' : 'All directions unlocked · priority creation'),
        isFree && h('button', { class: 'btn', style: { marginTop: '12px', height: '42px', borderRadius: '10px', fontSize: '13.5px' }, onClick: () => openPaywall('profile') }, 'See plans')),
      h('div', { class: 'panel', style: { marginTop: '16px', overflow: 'hidden' } },
        rows.map(([label, onClick], i) => h('button', {
          style: {
            display: 'flex', alignItems: 'center', justifyContent: 'space-between', width: '100%', background: 'none', cursor: 'pointer',
            padding: '13px 16px', border: 'none', borderBottom: i < rows.length - 1 ? '1px solid rgba(217,213,204,.55)' : 'none',
            font: '500 13.5px var(--sans)', color: label === 'Delete account' ? 'var(--danger)' : 'var(--ink)',
          }, onClick,
        }, label, svg(icons.chev)))),
      h('div', { style: { textAlign: 'center', font: '400 10.5px var(--mono)', letterSpacing: '1px', color: 'var(--ink-muted)', padding: '24px 0 8px' } }, 'MUSEFRAME 0.1 · MVP'),
    ),
    tabbar('profile'),
  );
}

// ---------- paywall ----------
function openPaywall(context) {
  S.paywall = { context };
  track('paywall_viewed', { context });
  renderOverlay();
}
function PaywallSheet() {
  const plans = S.products.filter(p => p.productType === 'subscription');
  const mini = S.products.find(p => p.internalKey === 'mini_pack');
  const sel = plans.find(p => p.internalKey === S.payPlan) || plans[0];
  const price = (m) => `$${(m / 100).toFixed(2)}`;
  const buy = async (key) => {
    try {
      const res = await post('/v1/purchases/verify', { platform: 'web', productKey: key, transactionId: `web_${crypto.randomUUID()}` });
      S.ent = res.entitlements; S.paywall = null;
      render(); renderOverlay();
      toast(key === 'mini_pack' ? 'Mini Pack added — 8 images' : 'Welcome to Creator');
      track('purchase_completed', { productId: key });
    } catch { toast('Purchase failed — you were not charged'); }
  };
  return h('div', { class: 'sheet-backdrop', onClick: () => { S.paywall = null; renderOverlay(); } },
    h('div', { class: 'sheet', onClick: (e) => e.stopPropagation() },
      h('div', { class: 'sheet-grab' }),
      h('div', { style: { display: 'flex', alignItems: 'center', gap: '12px', paddingBottom: '8px' } },
        h('div', { style: { width: '44px', height: '55px', borderRadius: '8px', flex: 'none', background: artBg(S.draft.style) || 'linear-gradient(135deg,#1C49D8,#0A1C52)', backgroundSize: 'cover' } }),
        h('div', { style: { flex: 1, font: '600 19px var(--serif)' } }, 'Keep creating in this exhibition'),
        h('button', { class: 'iconbtn', style: { width: '30px', height: '30px', background: 'var(--canvas)', color: 'var(--ink-muted)', fontSize: '13px' }, onClick: () => { S.paywall = null; renderOverlay(); } }, '✕')),
      h('div', { style: { font: '400 13px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingBottom: '16px' } }, 'More directions, higher resolution, and priority creation.'),
      h('div', { style: { display: 'flex', flexDirection: 'column', gap: '10px' } },
        plans.map(p => h('div', {
          class: 'plan-row' + (S.payPlan === p.internalKey ? ' selected' : ''),
          onClick: () => { S.payPlan = p.internalKey; renderOverlay(); },
        },
          h('div', { class: 'radio' }),
          h('div', { style: { flex: 1 } },
            h('div', { style: { font: '600 14px var(--sans)' } }, p.displayName),
            h('div', { style: { font: '400 11.5px var(--sans)', color: 'var(--ink-muted)' } }, `${p.grantedUnits} units / ${p.period} · all directions`)),
          p.period === 'year' && h('span', { style: { font: '600 10px var(--sans)', color: 'var(--success)', background: 'var(--success-soft)', borderRadius: '999px', padding: '3px 8px' } }, 'SAVE 42%'),
          h('div', { style: { font: '600 14px var(--sans)' } }, price(p.priceMinor))))),
      h('button', { class: 'btn', style: { marginTop: '14px', height: '50px', fontSize: '15px' }, onClick: () => buy(sel.internalKey) },
        `Continue — ${price(sel.priceMinor)} / ${sel.period}`),
      mini && h('button', { class: 'linkbtn', style: { width: '100%', textAlign: 'center', paddingTop: '14px', color: 'var(--ink)', fontSize: '12.5px' }, onClick: () => buy('mini_pack') },
        `Not ready? Mini Pack — 8 images for ${price(mini.priceMinor)}`),
      h('button', { class: 'linkbtn', style: { width: '100%', textAlign: 'center', paddingTop: '10px', fontWeight: 500, fontSize: '12px' }, onClick: async () => { await refreshEnt(); render(); toast(S.ent.plan === 'free' ? 'No previous purchases found' : 'Purchases restored'); } }, 'Restore purchases'),
      h('div', { style: { textAlign: 'center', font: '400 10px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '12px' } }, 'Demo checkout — no real charge. Auto-renews until cancelled. Terms · Privacy'),
    ),
  );
}

// ---------- style detail & info sheets ----------
function StyleDetailSheet() {
  const c = S.detail;
  const locked = c.premium && S.ent?.plan === 'free';
  return h('div', { class: 'sheet-backdrop', onClick: () => { S.detail = null; renderOverlay(); } },
    h('div', { class: 'sheet', onClick: (e) => e.stopPropagation() },
      h('div', { class: 'sheet-grab' }),
      h('div', { style: { display: 'flex', gap: '14px', alignItems: 'flex-start' } },
        h('div', { style: { width: '96px', aspectRatio: '4/5', borderRadius: '10px', flex: 'none', background: artBg(c), backgroundSize: 'cover' } }),
        h('div', { style: { flex: 1 } },
          h('div', { style: { font: '600 22px var(--serif)' } }, c.name),
          h('div', { style: { font: '400 13px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '4px' } }, c.shortCaption),
          h('div', { class: 'card-tags', style: { paddingTop: '8px' } }, 'Works best with — ' + c.suitabilityTags.join(' · ')),
          c.premium && h('div', { style: { paddingTop: '8px' } }, h('span', { style: { font: '600 9px var(--sans)', letterSpacing: '.6px', background: 'var(--canvas)', border: '1px solid var(--line)', borderRadius: '999px', padding: '3px 7px' } }, 'PREMIUM')))),
      h('div', { style: { font: '400 12.5px/1.6 var(--sans)', color: 'var(--ink-muted)', padding: '14px 0' } },
        'An original MuseFrame direction. Identity, pose and key objects are preserved; light, color and texture follow the direction. May change: background detail, fine texture.'),
      locked && h('div', { style: { font: '500 11.5px/1.5 var(--sans)', color: 'var(--warning)', background: 'var(--warning-soft)', borderRadius: '10px', padding: '9px 12px', marginBottom: '12px' } },
        'Premium direction — included with Creator.'),
      h('button', {
        class: 'btn', onClick: () => {
          S.draft.style = c; S.detail = null; renderOverlay();
          S.draft.assetId ? go('configure') : startImport(S.screen);
        },
      }, 'Use this direction'),
    ),
  );
}

const INFO = {
  privacy: ['Privacy & data', 'Your photos are used only to create the results you request. Uploads are re-encoded on your device, which removes location and camera metadata. Source photos are kept for 30 days by default; results stay until you delete them. Deleting a project removes its images from storage.'],
  about: ['About our styles', 'All 24 directions are original MuseFrame StyleSpecs, organized in six curated exhibitions. Each is a versioned, tested product asset built from public-domain visual principles — no third-party prompt packs, no living artists’ names. Published versions are immutable: your old works always re-render the version they used.'],
  delete: ['Delete account', 'In the shipping product this starts an asynchronous deletion workflow: sign-in is revoked, images enter a purge queue, and legally required payment records are separated and de-identified. In this demo you can clear this device’s session from your browser storage.'],
  purchases: null, // rendered dynamically
};
function openInfo(key) { S.infoSheet = key; renderOverlay(); }
function InfoSheet() {
  const key = S.infoSheet;
  let title, body;
  if (key === 'purchases') {
    title = 'Purchase history';
    body = S.purchases.length
      ? h('div', null, S.purchases.map(p => h('div', { style: { display: 'flex', justifyContent: 'space-between', padding: '8px 0', borderBottom: '1px solid rgba(217,213,204,.5)', font: '400 13px var(--sans)' } },
        h('span', null, p.product), h('span', { style: { color: 'var(--ink-muted)' } }, `$${(p.amountMinor / 100).toFixed(2)} · ${new Date(p.purchasedAt).toLocaleDateString()}`))))
      : 'No purchases yet.';
  } else [title, body] = INFO[key];
  return h('div', { class: 'sheet-backdrop', onClick: () => { S.infoSheet = null; renderOverlay(); } },
    h('div', { class: 'sheet', onClick: (e) => e.stopPropagation() },
      h('div', { class: 'sheet-grab' }),
      h('div', { style: { font: '600 20px var(--serif)', paddingBottom: '10px' } }, title),
      h('div', { style: { font: '400 13px/1.65 var(--sans)', color: 'var(--ink-muted)' } }, body),
      h('button', { class: 'btn secondary', style: { marginTop: '18px' }, onClick: () => { S.infoSheet = null; renderOverlay(); } }, 'Close'),
    ),
  );
}

// ---------- render ----------
const SCREENS = {
  onboarding: OnboardingScreen,
  discover: DiscoverScreen,
  exhibition: ExhibitionScreen,
  import: ImportScreen,
  styles: StylesScreen,
  configure: ConfigureScreen,
  progress: ProgressScreen,
  result: ResultScreen,
  projects: ProjectsScreen,
  profile: ProfileScreen,
};

function render() {
  app.replaceChildren(SCREENS[S.screen]?.() || DiscoverScreen());
  renderOverlay();
}
function renderOverlay() {
  const parts = [];
  if (S.analyzing) parts.push(h('div', { style: { position: 'absolute', inset: 0, background: 'rgba(247,245,239,.92)', display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: '14px', backdropFilter: 'blur(6px)', zIndex: 70 } },
    h('div', { class: 'spinner' }),
    h('div', { style: { font: '600 20px var(--serif)' } }, 'Reading the image'),
    h('div', { style: { font: '400 12.5px var(--sans)', color: 'var(--ink-muted)' } }, 'Subject, light and composition — a moment.')));
  if (S.detail) parts.push(StyleDetailSheet());
  if (S.infoSheet) parts.push(InfoSheet());
  if (S.paywall) parts.push(PaywallSheet());
  if (S.toast) parts.push(h('div', { class: 'toast' }, S.toast));
  overlayRoot.replaceChildren(...parts);
}

// ---------- boot ----------
async function bootApp() {
  S.bootError = false;
  render(); // paint loader immediately
  try {
    await ensureSession();
    await loadCore();
    track('discover_viewed', {});
  } catch (e) {
    S.bootError = true;
  }
  S.booted = true;
  render();
}
bootApp();
