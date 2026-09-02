// MuseFrame web app — curated-gallery P0 flow (spec §6):
// Onboarding → Discover → Exhibition → Styles → Preview settings → Progress → Result,
// plus Projects, Profile, Auth (email code), Paywall. Vanilla DOM, no build step.
//
// 2026-09: two presentations of the same screens — `phone` (native app and
// mobile browsers: tab bar, bottom sheets) and `web` (desktop browsers: sticky
// top nav, centred column, wrapped grids, centred dialogs). Chinese / English
// copy via i18n.js. Free tier = N artworks after email registration; when they
// are used up the paywall asks the user to email support (manual top-up).
import { ensureSession, setToken, clearToken, get, post, put, del, assetUrl, apiUrl, track } from './api.js';
import { deviceId, getAuthConfig, nativeSignIn, nativePurchase, isNative, platform, emailRequestCode, emailVerifyCode } from './native.js';
import { t, getLang, setLang, initLang } from './i18n.js?v=20260902b';

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
  mail: '<svg width="16" height="13" viewBox="0 0 16 13" fill="none"><rect x="1" y="1" width="14" height="11" rx="2" stroke="currentColor" stroke-width="1.5"/><path d="M1.5 2.5L8 7.5l6.5-5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
};

// ---------- layout: phone vs web ----------
const params = new URLSearchParams(location.search);
const WEB_MQ = window.matchMedia('(min-width: 900px)');
function computeLayout() {
  if (isNative()) return 'phone';
  const forced = params.get('ui');
  if (forced === 'phone' || forced === 'web') return forced;
  return WEB_MQ.matches ? 'web' : 'phone';
}
const isWeb = () => S.layout === 'web';
function applyLayout() {
  document.documentElement.classList.toggle('web', isWeb());
}
WEB_MQ.addEventListener('change', () => {
  if (isNative() || params.get('ui')) return;
  const next = computeLayout();
  if (next === S.layout) return;
  S.layout = next; applyLayout();
  if (S.screen === 'onboarding' && next === 'web') S.screen = 'discover';
  render();
});

// ---------- state ----------
const S = {
  layout: computeLayout(),
  screen: 'discover',
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
  projects: [], projTab: 'all',
  purchases: [],
  paywall: null,           // {context}
  payPlan: 'creator_monthly',
  detail: null,            // style card for detail sheet
  infoSheet: null,         // key
  analyzing: false,
  toast: null,
  booted: false,
  authReturn: 'discover',  // where to go after sign-in
  emailLogin: null,
};
// Onboarding is a phone-app ritual; the website goes straight to the gallery.
if (S.layout === 'phone' && !localStorage.getItem('mf.onboarded')) S.screen = 'onboarding';
applyLayout();

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
  if (isWeb()) window.scrollTo({ top: 0 });
}

// ---------- session helpers ----------
const signedIn = () => localStorage.getItem('mf.signedIn') === '1';
const userLabel = () => localStorage.getItem('mf.userName') || '';
const userEmail = () => localStorage.getItem('mf.userEmail') || '';
const freeUnits = () => S.authConfig?.freeUnits ?? 3;
const supportEmail = () => S.authConfig?.support?.email || 'donaldkuke@gmail.com';
const supportQQ = () => S.authConfig?.support?.qqGroup || '';
const freeNeedsAuth = () => S.authConfig?.freeRequiresAuth !== false;
const nativeBilling = () => isNative() && !!(S.authConfig?.billing?.google || S.authConfig?.billing?.apple) && S.products.length > 0;

// Catalogue helpers: Chinese UI shows the CNY price, English the USD price.
// A product with no CNY price is simply not offered to Chinese-language users.
function productPrice(p) {
  if (getLang() === 'zh') return p.priceCnyMinor != null ? `¥${(p.priceCnyMinor / 100).toFixed(p.priceCnyMinor % 100 ? 2 : 0)}` : null;
  return p.priceMinor != null ? `$${(p.priceMinor / 100).toFixed(2)}` : null;
}
const productName = (p) => (getLang() === 'zh' ? (p.displayNameZh || p.displayName) : p.displayName);
const offeredProducts = () => S.products.filter(p => productPrice(p) !== null);
function perImage(p) {
  if (!p.grantedUnits) return null;
  const minor = getLang() === 'zh' ? p.priceCnyMinor : p.priceMinor;
  if (minor == null) return null;
  const v = minor / 100 / p.grantedUnits;
  return (getLang() === 'zh' ? '¥' : '$') + (v < 1 ? v.toFixed(2) : v.toFixed(1));
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
  if (S.ent.plan === 'free') {
    if (u <= 0 && !signedIn() && freeNeedsAuth()) return t('Sign up · {n} free', { n: freeUnits() });
    return u > 0 ? t('{n} left', { n: u }) : t('No artworks left');
  }
  return t('{n} left', { n: u });
}
const fmtDate = (iso, opts = { month: 'short', day: 'numeric' }) =>
  new Date(iso).toLocaleDateString(getLang() === 'zh' ? 'zh-CN' : 'en-US', opts);

// ---------- shared components ----------
function topbar(title, onBack, opts = {}) {
  return h('div', { class: 'topbar' + (opts.clear ? ' clear' : '') },
    onBack
      ? h('button', { class: 'iconbtn', 'aria-label': t('Back'), onClick: onBack }, svg(icons.back))
      : h('div', { style: { width: '38px' } }),
    h('div', { class: 'topbar-title' + (opts.serif ? ' serif' : '') }, title),
    opts.right || h('div', { style: { width: '38px' } }),
  );
}

function tabbar(active) {
  const tab = (id, icon, label, onClick) =>
    h('button', { class: 'tab' + (active === id ? ' active' : ''), onClick }, svg(icon), h('div', null, label));
  return h('div', { class: 'tabbar' },
    tab('discover', icons.discover, t('Discover'), () => go('discover')),
    h('button', { class: 'tab', onClick: () => startImport(S.screen) },
      h('div', { class: 'create-dot' }, svg(icons.plus)), h('div', null, t('Create'))),
    tab('projects', icons.projects, t('Projects'), () => { loadProjects(); go('projects'); }),
    tab('profile', icons.profile, t('Profile'), () => { loadProfile(); go('profile'); }),
  );
}

// Website chrome: brand, section nav, language, quota badge, sign-in, create.
function webnav(active) {
  const nav = (id, label, onClick) => h('button', { class: active === id ? 'active' : '', onClick }, label);
  return h('header', { class: 'webnav' },
    h('button', { class: 'brand', onClick: () => go('discover') }, 'MUSEFRAME'),
    h('nav', null,
      nav('discover', t('Discover'), () => go('discover')),
      nav('projects', t('Projects'), () => { loadProjects(); go('projects'); }),
      nav('profile', t('Profile'), () => { loadProfile(); go('profile'); }),
    ),
    h('div', { class: 'spacer' }),
    h('button', { class: 'lang', onClick: toggleLang, 'aria-label': 'Language' }, getLang() === 'zh' ? 'EN' : '中文'),
    S.ent && h('button', { class: 'pillbtn', onClick: () => openPaywall('badge') }, unitsBadgeText()),
    !signedIn() && h('button', { class: 'btn secondary cta', onClick: () => openAuth(S.screen) }, t('Sign in / Register')),
    h('button', { class: 'btn cta', onClick: () => startImport(S.screen) }, t('Create')),
  );
}

// Every screen: web → top nav first (+ footer on the main sections); phone → tab bar last (only tab screens).
function shell(active, ...children) {
  const extra = children.length && typeof children[children.length - 1] === 'object' && children[children.length - 1]?.screenClass
    ? children.pop().screenClass : '';
  return h('div', { class: 'screen' + (extra ? ' ' + extra : '') },
    isWeb() && webnav(active),
    ...children,
    isWeb() && active && webfooter(),
    !isWeb() && active && tabbar(active),
  );
}
function webfooter() {
  const link = (label, onClick, href) => h(href ? 'a' : 'button', { class: 'flink', href, onClick }, label);
  return h('footer', { class: 'webfooter' },
    h('div', { class: 'inner' },
      h('div', { class: 'brand' }, 'MUSEFRAME', h('span', null, t('Curated image making'))),
      h('nav', null,
        link(t('Pricing'), () => openPaywall('footer')),
        link(t('Privacy & data'), () => openInfo('privacy')),
        link(t('About our styles'), () => openInfo('about')),
        supportQQ() && link(t('QQ group {n}', { n: supportQQ() }), () => openPaywall('footer')),
        link(t('Contact us'), null, `mailto:${supportEmail()}`),
        link(t('Android app'), null, getLang() === 'zh' ? 'https://museframe.lenscript.cn/#download' : 'https://museframe.lenscript.cn/en/#download'),
        link(getLang() === 'zh' ? 'English' : '中文', toggleLang)),
      h('div', { class: 'fine' }, t('Photos are re-encoded on your device before upload; failed generations never use an artwork.'))));
}

function toggleLang() {
  setLang(getLang() === 'zh' ? 'en' : 'zh');
  render();
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
    showTags && h('div', { class: 'card-tags' }, t('Best with — ') + card.suitabilityTags.map(tagLabel).join(' · ')),
  );
}
const tagLabel = (tag) => t('tag.' + tag);

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

// ---------- onboarding (phone only) ----------
const OB = () => [
  { title: t('Your photos, newly seen.'), body: t('One photo in, one artwork out — identity kept, direction clear.'), art: 'linear-gradient(160deg,#C9BFB2 0%,#8F8275 55%,#4E463D 100%)' },
  { title: t('Choose a direction, not a prompt.'), body: t('Curated exhibitions and style cards replace prompt engineering.'), art: 'linear-gradient(145deg,#F1EEE4 0%,#F1EEE4 44%,#22335F 44%,#22335F 72%,#B4432E 72%,#B4432E 100%)' },
  { title: t('Private by default.'), body: t('Your photos are used only to create and improve your requested result. Location data is removed.'), art: 'linear-gradient(170deg,#EBECEC 0%,#C0C5C8 50%,#8F979D 100%)' },
];
function OnboardingScreen() {
  const pages = OB();
  const p = pages[S.obPage];
  const finish = (dest) => {
    localStorage.setItem('mf.onboarded', '1');
    track('onboarding_completed', {});
    dest === 'import' ? startImport('discover') : go('discover');
  };
  return h('div', { class: 'screen' },
    h('div', { class: 'scroll', style: { display: 'flex', flexDirection: 'column', padding: '60px 24px 24px' } },
      h('div', { style: { font: '600 22px var(--serif)', letterSpacing: '6px', textAlign: 'center', paddingBottom: '6px' } }, 'MUSEFRAME'),
      h('div', { class: 'kicker', style: { textAlign: 'center', paddingBottom: '26px' } }, t('Curated image making')),
      h('div', { style: { borderRadius: '14px', border: '1px solid var(--line)', aspectRatio: '4/5', background: 'repeating-linear-gradient(115deg,rgba(23,23,23,.03) 0 2px,rgba(255,255,255,.02) 2px 4px),' + p.art } }),
      h('div', { style: { font: '600 26px/1.2 var(--serif)', padding: '22px 0 8px' } }, p.title),
      h('div', { style: { font: '400 14px/1.55 var(--sans)', color: 'var(--ink-muted)' } }, p.body),
      h('div', { style: { display: 'flex', gap: '6px', padding: '18px 0', justifyContent: 'center' } },
        pages.map((_, i) => h('div', { style: { width: '7px', height: '7px', borderRadius: '99px', background: i === S.obPage ? 'var(--cobalt)' : 'var(--line)' } }))),
      h('div', { style: { flex: 1 } }),
      S.obPage < pages.length - 1
        ? h('button', { class: 'btn', onClick: () => { S.obPage++; render(); } }, t('Continue'))
        : h('button', { class: 'btn', onClick: () => finish('import') }, t('Choose a photo')),
      h('button', { class: 'linkbtn', style: { padding: '14px 0 4px', textAlign: 'center', width: '100%' }, onClick: () => finish('discover') }, t('Explore first')),
      h('button', { class: 'linkbtn', style: { padding: '10px 0 0', textAlign: 'center', width: '100%', color: 'var(--ink-muted)', fontWeight: 500 }, onClick: toggleLang }, getLang() === 'zh' ? 'English' : '中文'),
    ),
  );
}

// ---------- discover ----------
function DiscoverScreen() {
  const d = S.discover;
  if (!d) {
    // No dead spinners (spec §6.1): connection failure gets a clear message + retry.
    if (S.bootError) return shell('discover',
      h('div', { style: { margin: 'auto', textAlign: 'center', padding: '60px 40px' } },
        h('div', { style: { font: '600 22px var(--serif)', paddingBottom: '8px' } }, t('Can’t reach the gallery')),
        h('div', { style: { font: '400 13px/1.6 var(--sans)', color: 'var(--ink-muted)', paddingBottom: '20px' } },
          t('Check your connection and try again. Your drafts and works are safe.')),
        h('button', { class: 'btn', style: { width: 'auto', padding: '0 28px', margin: '0 auto' }, onClick: bootApp }, t('Try again'))));
    return shell(null, h('div', { style: { margin: 'auto', padding: '80px 0' } }, h('div', { class: 'spinner' })));
  }
  const hero = d.heroExhibition;
  const guestNudge = !signedIn() && freeNeedsAuth() && S.ent?.plan === 'free' && (S.ent?.availableUnits || 0) === 0;
  return shell('discover',
    h('div', { class: 'scroll', style: { paddingBottom: '110px' } },
      h('div', { style: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: 'max(22px, env(safe-area-inset-top)) 20px 2px' } },
        h('div', { class: 'kicker' }, `${hero.edition} · ${t('AUG 2026')}`),
        !isWeb() && h('button', { class: 'pillbtn', onClick: () => openPaywall('badge') }, unitsBadgeText()),
      ),
      h('div', { class: 'page-title', style: { padding: '4px 20px 16px' } }, t('Discover')),
      // hero exhibition
      h('div', { style: { padding: isWeb() ? 0 : '0 20px' } },
        h('div', { class: 'hero' },
          h('div', { class: 'hero-art', style: { background: artBg(hero.styles[0]) } }),
          h('div', { class: 'hero-shade' }),
          h('div', { class: 'hero-text' },
            h('div', { class: 'hero-kicker' }, t('EXHIBITION · {n} STYLES', { n: hero.styles.length })),
            h('div', { class: 'hero-title' }, hero.title),
            h('div', { class: 'hero-note' }, hero.curatorialNote),
            h('button', {
              class: 'hero-btn',
              onClick: () => { S.exhibition = hero.slug; track('exhibition_opened', { slug: hero.slug }); go('exhibition'); },
            }, t('View exhibition')),
          ),
        ),
      ),
      // registration nudge (free tier needs an account)
      guestNudge && h('div', { style: { margin: isWeb() ? '14px 0 0' : '14px 20px 0', display: 'flex', alignItems: 'center', gap: '12px', background: 'var(--cobalt-soft)', border: '1px solid rgba(28,73,216,.25)', borderRadius: '14px', padding: '12px 14px' } },
        h('div', { style: { flex: 1, minWidth: 0 } },
          h('div', { style: { font: '600 13.5px var(--sans)', color: 'var(--cobalt)' } }, t('{n} free artworks when you register', { n: freeUnits() })),
          h('div', { style: { font: '400 11.5px/1.4 var(--sans)', color: 'var(--ink-muted)' } }, t('Email only — no password, no card.')),
        ),
        h('button', { class: 'btn small', style: { width: 'auto', padding: '0 16px', flex: 'none' }, onClick: () => openAuth('discover') }, t('Register')),
      ),
      // for-your-photo card
      h('div', { style: { margin: isWeb() ? '14px 0 0' : '14px 20px 0', display: 'flex', alignItems: 'center', gap: '12px', background: 'var(--surface)', border: '1px solid var(--line)', borderRadius: '14px', padding: '12px 14px' } },
        h('div', { style: { width: '46px', height: '46px', borderRadius: '10px', flex: 'none', background: S.draft.previewUrl ? `url(${S.draft.previewUrl}) center/cover` : 'linear-gradient(170deg,#B9AE9A 0%,#8E8878 45%,#544F45 100%)', filter: S.draft.previewUrl ? 'none' : 'blur(1px)' } }),
        h('div', { style: { flex: 1, minWidth: 0 } },
          h('div', { style: { font: '600 13.5px var(--sans)' } }, t('For your photo')),
          h('div', { style: { font: '400 11.5px/1.4 var(--sans)', color: 'var(--ink-muted)' } }, t('Directions matched to your shot.')),
        ),
        h('button', { class: 'linkbtn', style: { flex: 'none' }, onClick: () => startImport('discover') }, t('Find a direction')),
      ),
      // shelves
      d.shelves.map(sh => h('div', { style: { marginTop: '30px' } },
        h('div', { style: { display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', padding: isWeb() ? '0 0 12px' : '0 20px 10px' } },
          h('div', { style: { font: '600 20px var(--serif)' } }, sh.title),
          h('button', { class: 'linkbtn', style: { fontWeight: 500, fontSize: '12px' }, onClick: () => { S.exhibition = sh.slug; go('exhibition'); } }, t('View all')),
        ),
        h('div', { class: 'hrow' },
          sh.styles.map(c => styleCardEl(c, { width: '146px', onClick: () => selectStyle(c, 'discover') }))),
      )),
      h('div', { style: { textAlign: 'center', font: '400 11px var(--sans)', color: 'var(--ink-muted)', padding: '34px 20px 8px' } },
        h('button', { class: 'linkbtn', style: { color: 'var(--ink-muted)', fontWeight: 400, fontSize: '11px' }, onClick: () => openInfo('about') }, t('About our styles')), ' · ',
        h('button', { class: 'linkbtn', style: { color: 'var(--ink-muted)', fontWeight: 400, fontSize: '11px' }, onClick: () => openInfo('privacy') }, t('Privacy')), ' · ',
        h('a', { class: 'linkbtn', style: { color: 'var(--ink-muted)', fontWeight: 400, fontSize: '11px', textDecoration: 'none' }, href: `mailto:${supportEmail()}` }, t('Contact us')),
        supportQQ() && [' · ', h('button', { class: 'linkbtn', style: { color: 'var(--ink-muted)', fontWeight: 400, fontSize: '11px' }, onClick: () => openPaywall('discover') }, t('QQ group {n}', { n: supportQQ() }))],
      ),
    ),
  );
}

// ---------- exhibition ----------
function ExhibitionScreen() {
  const exh = allShelves().find(s => s.slug === S.exhibition) || S.discover.heroExhibition;
  return shell(null,
    topbar(t('Exhibition'), () => go('discover')),
    h('div', { class: 'scroll reading', style: { padding: '14px 20px 120px' } },
      h('div', { style: { position: 'relative', borderRadius: '14px', overflow: 'hidden', border: '1px solid var(--line)', aspectRatio: isWeb() ? '21/9' : '16/10', background: artBg(exh.styles[0]) } },
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
      h('button', { class: 'btn', onClick: () => startImport('exhibition') }, t('Choose a photo'))),
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
  const dropHandlers = isWeb() ? {
    onDragOver: (e) => { e.preventDefault(); e.currentTarget.style.borderColor = 'var(--cobalt)'; },
    onDragLeave: (e) => { e.currentTarget.style.borderColor = 'var(--line)'; },
    onDrop: (e) => { e.preventDefault(); const f = e.dataTransfer?.files?.[0]; if (f) handleFile(f); },
  } : {};
  return shell(null,
    topbar(t('Choose a photo'), () => go(S.importFrom === 'exhibition' ? 'exhibition' : 'discover')),
    h('div', { class: 'scroll narrow', style: { padding: '18px 20px 40px' } },
      fileInput, cameraInput,
      h('div', { class: 'kicker', style: { paddingBottom: '10px' } }, t('Photo library')),
      h('div', {
        style: { border: '1px dashed var(--line)', borderRadius: '14px', background: 'var(--surface)', padding: isWeb() ? '64px 20px' : '38px 20px', textAlign: 'center', cursor: 'pointer' },
        onClick: () => fileInput.click(), ...dropHandlers,
      },
        h('div', { style: { font: '600 17px var(--serif)' } }, isWeb() ? t('Drop a photo here, or browse') : t('Choose from your photos')),
        h('div', { style: { font: '400 12.5px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '6px' } }, t('JPEG, PNG or HEIC · up to 20 MB')),
        h('div', { style: { paddingTop: '14px' } }, h('span', { class: 'chip active' }, t('Browse'))),
      ),
      !isWeb() && [
        h('div', { style: { display: 'flex', alignItems: 'center', gap: '12px', padding: '22px 0 14px' } },
          h('div', { style: { flex: 1, height: '1px', background: 'var(--line)' } }),
          h('div', { style: { font: '400 11px var(--sans)', color: 'var(--ink-muted)' } }, t('or')),
          h('div', { style: { flex: 1, height: '1px', background: 'var(--line)' } })),
        h('button', { class: 'btn secondary', onClick: () => cameraInput.click() }, svg(icons.camera), t('Take a photo')),
      ],
      h('div', { style: { display: 'flex', alignItems: 'flex-start', gap: '8px', paddingTop: '20px', color: 'var(--ink-muted)' } },
        svg(icons.lock, ''),
        h('div', { style: { font: '400 11px/1.55 var(--sans)' } }, t('Location data is removed. Your photo is used only to create the result you request, then follows your retention setting.'))),
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
    if (file.size > 20 * 1024 * 1024) { toast(t('Images up to 20 MB are supported')); S.analyzing = false; renderOverlay(); return; }
    const { blob, width, height } = await toJpeg(file);
    if (Math.min(width, height) < 320) { toast(t('This photo is too small to style well')); S.analyzing = false; renderOverlay(); return; }
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
    if (Math.min(width, height) < 768) toast(t('Low resolution — quality may be reduced'), 2500);
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
    else if (e.message === 'decode' || e.message === 'unsupported') toast(t('Could not read that image — try another'));
    else toast(t('Connection problem — check your network and try again'), 2600);
  }
}

// ---------- styles ----------
const FILTERS = ['all', 'portrait', 'landscape', 'object', 'pet'];
function StylesScreen() {
  const a = S.draft.analysis;
  const recs = (a?.recommendations || [])
    .map(r => ({ card: findStyle(c => c.styleVersionId === r.styleVersionId), reason: r.reasonCode.replaceAll('_', ' ') }))
    .filter(r => r.card);
  const subjectLabel = a?.status === 'ready'
    ? `${tagLabel(a.subjectType)}${a.subjectType === 'person' ? `, ${t('{n} person', { n: a.personCount })}` : ''}`
    : t('reading…');
  const match = (c) => S.filter === 'all' || c.suitabilityTags.includes(S.filter);
  const pad = isWeb() ? '0' : '0 20px';
  return shell(null,
    topbar(t('Choose a direction'), () => go('import')),
    h('div', { class: 'scroll', style: { paddingBottom: '130px' } },
      // photo chip
      h('div', { style: { display: 'flex', alignItems: 'center', gap: '10px', margin: isWeb() ? '0' : '14px 20px 0', background: 'var(--surface)', border: '1px solid var(--line)', borderRadius: '12px', padding: '9px 12px' } },
        h('div', { style: { width: '34px', height: '34px', borderRadius: '8px', flex: 'none', background: `url(${S.draft.previewUrl}) center/cover` } }),
        h('div', { style: { flex: 1, font: '500 12.5px var(--sans)' } }, t('For this photo '), h('span', { style: { color: 'var(--ink-muted)', fontWeight: 400 } }, `· ${subjectLabel}`)),
        h('button', { class: 'linkbtn', onClick: () => go('import') }, t('Change')),
      ),
      // filters
      h('div', { style: { display: 'flex', gap: '8px', padding: isWeb() ? '14px 0 0' : '14px 20px 0', overflowX: 'auto' } },
        FILTERS.map((key) => h('button', {
          class: 'chip' + (S.filter === key ? ' active' : ''),
          onClick: () => { S.filter = key; render(); },
        }, key === 'all' ? t('All') : tagLabel(key)))),
      // recommended
      recs.length > 0 && [
        h('div', { class: 'kicker', style: { padding: isWeb() ? '22px 0 10px' : '22px 20px 10px' } }, t('Recommended')),
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
          h('div', { style: { font: '600 19px var(--serif)', padding: pad, paddingBottom: '2px' } }, sh.title),
          h('div', { style: { font: '400 12px/1.5 var(--sans)', color: 'var(--ink-muted)', padding: pad, paddingBottom: '12px' } }, sh.curatorialNote),
          h('div', { class: 'grid2', style: { padding: pad } },
            cards.map(c => styleCardEl(c, {
              selected: S.draft.style?.styleId === c.styleId,
              onClick: () => { S.draft.style = c; render(); },
            }))),
        );
      }),
    ),
    S.draft.style && h('div', { class: 'bottom-bar', style: { display: 'flex', alignItems: 'center', gap: '12px', background: 'rgba(255,255,255,.94)' } },
      h('div', { style: { flex: 1, minWidth: 0 } },
        h('div', { class: 'kicker', style: { fontSize: '10px', letterSpacing: '1.4px' } }, t('Selected')),
        h('div', { style: { font: '600 14px var(--serif)' } }, S.draft.style.name)),
      h('button', { class: 'linkbtn', onClick: () => { S.detail = S.draft.style; renderOverlay(); } }, t('Details')),
      h('button', { class: 'btn', style: { width: 'auto', height: '46px', padding: '0 20px', fontSize: '14.5px', margin: 0 }, onClick: () => go('configure') }, t('Preview settings')),
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
      }, labels ? labels[i] : t('opt.' + v)))),
  );
  const premiumNote = card.premium && S.ent?.plan === 'free';
  const needsAccount = !signedIn() && freeNeedsAuth() && (S.ent?.availableUnits || 0) <= 0;
  return shell(null,
    topbar(t('Preview settings'), () => go('styles')),
    h('div', { class: 'scroll narrow', style: { padding: '18px 20px 120px' } },
      h('div', { class: 'panel', style: { display: 'flex', alignItems: 'center', gap: '14px', padding: '14px' } },
        h('div', { style: { display: 'flex', flexDirection: 'column', gap: '6px', alignItems: 'center' } },
          h('div', { style: { width: '64px', height: '80px', borderRadius: '10px', background: `url(${d.previewUrl}) center/cover` } }),
          h('div', { style: { font: '500 9px var(--mono)', letterSpacing: '1px', color: 'var(--ink-muted)' } }, t('YOUR PHOTO'))),
        h('div', { html: '<svg width="18" height="12" viewBox="0 0 18 12" fill="none"><path d="M1 6h15M12 1.5L16.5 6 12 10.5" stroke="#6E6B66" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>', style: { flex: 'none', display: 'flex' } }),
        h('div', { style: { display: 'flex', flexDirection: 'column', gap: '6px', alignItems: 'center' } },
          h('div', { style: { width: '64px', height: '80px', borderRadius: '10px', background: artBg(card), backgroundSize: 'cover' } }),
          h('div', { style: { font: '500 9px var(--mono)', letterSpacing: '1px', color: 'var(--ink-muted)' } }, t('DIRECTION'))),
        h('div', { style: { flex: 1, minWidth: 0, paddingLeft: '2px' } },
          h('div', { style: { font: '600 16px var(--serif)' } }, card.name),
          h('div', { style: { font: '400 11.5px/1.45 var(--sans)', color: 'var(--ink-muted)', paddingTop: '2px' } }, card.shortCaption)),
      ),
      premiumNote && h('div', { style: { marginTop: '12px', font: '500 11.5px/1.5 var(--sans)', color: 'var(--warning)', background: 'var(--warning-soft)', borderRadius: '10px', padding: '9px 12px' } },
        t('Premium direction — opened on request. You can preview settings freely.')),
      seg(t('Style strength'), 'strength', ['soft', 'balanced', 'bold']),
      seg(t('Subject fidelity'), 'fidelity', ['high', 'natural']),
      seg(t('Composition'), 'composition', ['keep', 'reframe']),
      seg(t('Output ratio'), 'ratio', ['original', '1:1', '4:5', '16:9'], [t('Original'), '1:1', '4:5', '16:9']),
      h('div', { style: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginTop: '24px', borderTop: '1px solid var(--line)', paddingTop: '14px' } },
        h('div', { class: 'kicker', style: { letterSpacing: '1.2px' } }, estimateLabel()),
        h('div', { style: { font: '500 11px var(--sans)', color: 'var(--ink-muted)' } }, unitsBadgeText())),
      needsAccount && h('div', { style: { marginTop: '14px', font: '400 12.5px/1.55 var(--sans)', color: 'var(--ink-muted)', background: 'var(--cobalt-soft)', borderRadius: '10px', padding: '10px 12px' } },
        t('Register with your email to receive {n} free artworks — the first one is this photo.', { n: freeUnits() })),
    ),
    h('div', { class: 'bottom-bar' },
      generationOffline()
        ? h('button', { class: 'btn', disabled: true, style: { opacity: .55 } }, t('Generating is paused'))
        : needsAccount
          ? h('button', { class: 'btn', onClick: () => openAuth('configure') }, t('Register to get {n} free artworks', { n: freeUnits() }))
          : h('button', { class: 'btn', onClick: generate }, t('Generate'))),
  );
}

// The server refuses jobs when no image model is configured (503
// GENERATION_UNAVAILABLE). Say so before the tap instead of after.
function generationOffline() {
  const g = S.discover?.generation || S.authConfig?.generation;
  return !!g && g.available === false;
}

// ---------- generation ----------
function estimateLabel() {
  if (generationOffline()) return t('Generating is paused — nothing will be charged');
  const r = S.lastEstimate || S.discover?.generation?.estimatedRangeSeconds;
  if (r && r[1] > 120) return t('Estimate — 1 artwork · {a}–{b} min', { a: Math.round(r[0] / 60), b: Math.round(r[1] / 60) });
  return t('Estimate — 1 artwork · 20–90 s');
}
let pollTimer = null;
async function generate() {
  const d = S.draft;
  if (generationOffline()) { toast(t('Generating is paused right now — nothing used'), 2600); return; }
  if (!d.projectId || !d.assetId) { startImport(S.screen); return; }
  if (!signedIn() && freeNeedsAuth() && (S.ent?.availableUnits || 0) <= 0) { openAuth('configure'); return; }
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
    else if (e.code === 'GENERATION_UNAVAILABLE') {
      // Key was cleared while the app was open — reflect it and re-render.
      if (S.discover?.generation) S.discover.generation.available = false;
      toast(t('Generating is paused right now — nothing used'), 2600);
      render();
    } else toast(e.message || t('Could not start — try again'));
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
        else { toast(t('Your artwork is ready — see Projects')); render(); }
      } else if (job.status === 'failed' || job.status === 'cancelled') {
        clearInterval(pollTimer);
        await refreshEnt();
        track('generation_failed', { jobId, errorCode: job.error?.code });
        if (S.screen === 'progress') {
          const code = job.error?.code;
          toast(code === 'GENERATION_REJECTED' ? t('This request can’t be created — nothing used')
            : code === 'GENERATION_UNAVAILABLE' ? t('Generating is paused right now — nothing used')
            : t('Something went wrong — nothing used'), 2600);
          go('configure');
        }
      } else if (S.screen === 'progress') render();
    } catch { /* transient poll error — keep trying */ }
  }, 2000);
}

const STAGES = () => [
  ['preparing', t('Preparing your photo')],
  ['building', t('Building the direction')],
  ['making', t('Making the image')],
  ['checking', t('Checking the result')],
];
function ProgressScreen() {
  const stages = STAGES();
  const stageIdx = Math.max(0, stages.findIndex(([k]) => k === S.job?.stage));
  return shell(null,
    h('div', { class: 'progress-bg', style: { background: S.draft.previewUrl ? `url(${S.draft.previewUrl}) center/cover` : 'var(--canvas)' } }),
    h('div', { class: 'progress-veil' }),
    h('div', { class: 'progress-body' },
      h('div', { class: 'spinner', style: { width: '34px', height: '34px' } }),
      h('div', { style: { font: '600 24px var(--serif)', padding: '18px 0 4px', textAlign: 'center' } }, stages[stageIdx][1]),
      h('div', { class: 'kicker', style: { paddingBottom: '26px' } }, t('Step {a} of {b}', { a: stageIdx + 1, b: 4 })),
      h('div', { class: 'panelish', style: { display: 'flex', flexDirection: 'column', gap: '11px', alignSelf: 'stretch', width: '100%', margin: '0 auto', background: 'rgba(255,255,255,.75)', border: '1px solid rgba(217,213,204,.8)', borderRadius: '14px', padding: '16px 18px' } },
        stages.map(([, label], i) => h('div', { style: { display: 'flex', alignItems: 'center', gap: '10px' } },
          h('div', { style: { width: '8px', height: '8px', borderRadius: '999px', flex: 'none', background: i < stageIdx ? 'var(--success)' : i === stageIdx ? 'var(--cobalt)' : 'var(--line)', animation: i === stageIdx ? 'mfPulse 1.1s ease infinite' : 'none' } }),
          h('div', { style: { font: '500 13px var(--sans)', color: i <= stageIdx ? 'var(--ink)' : 'var(--ink-muted)' } }, label)))),
      h('div', { style: { font: '400 12px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '22px', textAlign: 'center' } },
        S.jobStartedAt && S.lastEstimate && (Date.now() - S.jobStartedAt) / 1000 > S.lastEstimate[1]
          ? t('Still working — this one is taking longer than usual.')
          : t('You can leave — creation continues in the background.')),
      h('button', { class: 'linkbtn', style: { paddingTop: '10px', fontSize: '13px' }, onClick: () => go('discover') }, t('Back to Discover')),
    ),
    { screenClass: 'progress' },
  );
}

// ---------- result ----------
function ResultScreen() {
  const job = S.job;
  if (!job?.candidate) return ProgressScreen();
  const cand = job.candidate;
  const resultUrl = assetUrl(cand.assetId);
  const sourceUrl = S.draft.previewUrl || (S.draft.assetId ? assetUrl(S.draft.assetId) : null);
  const styleName = S.draft.style?.name || t('Result');

  const artBox = h('div', {
    style: {
      position: 'relative', borderRadius: '14px', overflow: 'hidden', border: '1px solid var(--line)',
      aspectRatio: `${cand.width}/${cand.height}`, cursor: 'pointer', userSelect: 'none', touchAction: 'none',
      maxHeight: isWeb() ? '78vh' : '58vh', margin: '0 auto',
    },
    onPointerDown: (e) => { if (!S.compareOn) { e.preventDefault(); S.hold = true; render(); } },
    onPointerUp: () => { if (S.hold) { S.hold = false; render(); } },
    onPointerLeave: () => { if (S.hold) { S.hold = false; render(); } },
  },
    sourceUrl && h('img', { src: sourceUrl, style: { position: 'absolute', inset: 0, width: '100%', height: '100%', objectFit: 'cover' }, alt: t('Original photo') }),
    h('img', {
      src: resultUrl, alt: `${t('Styled result')} — ${styleName}`,
      style: {
        position: 'absolute', inset: 0, width: '100%', height: '100%', objectFit: 'cover',
        opacity: S.hold ? 0 : 1, transition: 'opacity .16s',
        clipPath: S.compareOn ? `inset(0 0 0 ${S.compare}%)` : 'none',
      },
    }),
    S.compareOn && [
      h('div', { style: { position: 'absolute', top: 0, bottom: 0, left: `${S.compare}%`, width: '2px', background: 'rgba(255,255,255,.95)', boxShadow: '0 0 10px rgba(0,0,0,.4)' } }),
      h('div', { style: { position: 'absolute', top: '10px', left: '10px', font: '500 10px var(--sans)', color: '#fff', background: 'var(--overlay)', borderRadius: '6px', padding: '3px 8px' } }, t('Before')),
      h('div', { style: { position: 'absolute', top: '10px', right: '10px', font: '500 10px var(--sans)', color: '#fff', background: 'var(--overlay)', borderRadius: '6px', padding: '3px 8px' } }, t('After')),
    ],
    !S.compareOn && !S.hold && h('div', { style: { position: 'absolute', bottom: '10px', left: '50%', transform: 'translateX(-50%)', font: '500 10.5px var(--sans)', color: '#fff', background: 'var(--overlay)', borderRadius: '999px', padding: '5px 12px', whiteSpace: 'nowrap' } }, t('Hold to see original')),
  );

  const actions = h('div', null,
    h('div', { style: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '12px 2px 0' } },
      h('button', {
        class: 'chip', style: S.compareOn ? { background: 'var(--ink)', color: '#fff', borderColor: 'var(--ink)', display: 'flex', gap: '7px', alignItems: 'center' } : { display: 'flex', gap: '7px', alignItems: 'center' },
        onClick: () => { S.compareOn = !S.compareOn; S.hold = false; render(); },
      }, svg(icons.compare), t('Compare')),
      h('div', { style: { font: '500 9.5px var(--mono)', letterSpacing: '1px', color: 'var(--ink-muted)' } }, `${cand.width} × ${cand.height} · ${t('STANDARD')} · ${t('PRIVATE')}`)),
    S.compareOn && h('input', {
      type: 'range', min: 0, max: 100, value: S.compare, style: { marginTop: '12px' },
      onInput: (e) => { S.compare = +e.target.value; render(); },
    }),
    h('button', { class: 'btn' + (S.saved ? ' success' : ''), style: { marginTop: '14px' }, onClick: saveResult }, S.saved ? t('Saved ✓') : t('Save')),
    h('div', { style: { display: 'flex', gap: '10px', paddingTop: '10px' } },
      h('button', { class: 'btn secondary small', style: { flex: 1 }, onClick: shareResult }, t('Share')),
      h('button', { class: 'btn secondary small', style: { flex: 1 }, onClick: generate }, t('Try again')),
      h('button', { class: 'btn secondary small', style: { flex: 1 }, onClick: () => go('configure') }, t('Refine'))),
    FeedbackRow(cand.id),
  );

  return shell(null,
    topbar(styleName, () => go('discover'), { serif: true }),
    h('div', { class: 'scroll', style: { padding: '6px 16px 40px' } },
      h('div', { class: 'result-grid' }, artBox, actions),
    ),
  );
}

function FeedbackRow(candidateId) {
  if (S.fb === 'done') return h('div', { style: { textAlign: 'center', font: '400 12px var(--sans)', color: 'var(--success)', paddingTop: '20px' } }, t('Thanks — noted for this direction.'));
  if (S.fb === 'pick') {
    const reasons = [['FACE_CHANGED', t('Face changed')], ['WRONG_STYLE', t('Wrong style')], ['BAD_DETAILS', t('Bad details')], ['TOO_STRONG', t('Too strong')]];
    return h('div', { style: { display: 'flex', flexWrap: 'wrap', justifyContent: 'center', gap: '8px', paddingTop: '18px' } },
      reasons.map(([code, label]) => h('button', {
        class: 'chip', style: { fontWeight: 500, fontSize: '11.5px' },
        onClick: () => { post(`/v1/candidates/${candidateId}/feedback`, { rating: 'negative', reasonCodes: [code] }).catch(() => {}); S.fb = 'done'; render(); },
      }, label)));
  }
  return h('div', { style: { display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '10px', paddingTop: '20px' } },
    h('div', { style: { font: '400 12px var(--sans)', color: 'var(--ink-muted)' } }, t('How did it come out?')),
    h('button', { class: 'chip', onClick: () => { post(`/v1/candidates/${S.job.candidate.id}/feedback`, { rating: 'positive' }).catch(() => {}); S.fb = 'done'; render(); } }, t('Love it')),
    h('button', { class: 'chip', onClick: () => { S.fb = 'pick'; render(); } }, t('Not quite')));
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
    toast(t('Saved'));
    // The last free artwork is the moment to explain how to get more.
    if (S.ent?.plan === 'free' && (S.ent?.availableUnits || 0) === 0) setTimeout(() => openPaywall('post_save'), 1800);
  } catch (e) { toast(t('Could not save — try again')); }
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
  toast(t('Sharing not available here — use Save'));
}

// ---------- projects ----------
async function loadProjects() {
  try { S.projects = (await get('/v1/projects')).projects; render(); } catch { }
}
const STATUS_COLOR = { draft: ['#6E6B66', '#EFEDE6'], generating: ['#A36513', '#F6EBD9'], ready: ['#1C49D8', '#E8EDFF'], saved: ['#217A54', '#E3F0E9'] };
function ProjectsScreen() {
  const tabs = [['all', t('All')], ['generating', t('In progress')], ['saved', t('Saved')]];
  const filtered = S.projects.filter(p => {
    if (S.projTab === 'generating') return p.status === 'generating';
    if (S.projTab === 'saved') return p.status === 'saved';
    return true;
  });
  return shell('projects',
    h('div', { class: 'scroll', style: { padding: 'max(22px, env(safe-area-inset-top)) 20px 116px' } },
      h('div', { class: 'page-title', style: { padding: '14px 0' } }, t('Projects')),
      h('div', { style: { display: 'flex', gap: '8px', paddingBottom: '16px' } },
        tabs.map(([key, label]) => h('button', { class: 'chip' + (S.projTab === key ? ' active' : ''), onClick: () => { S.projTab = key; render(); } }, label))),
      filtered.length === 0
        ? h('div', { style: { textAlign: 'center', padding: '60px 20px', border: '1px dashed var(--line)', borderRadius: '14px' } },
          h('div', { style: { font: '600 17px var(--serif)' } }, t('Nothing here yet')),
          h('div', { style: { font: '400 12.5px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '6px' } }, t('Works you create will appear in this room.')),
          h('button', { class: 'btn small', style: { width: 'auto', padding: '0 18px', margin: '16px auto 0' }, onClick: () => startImport('projects') }, t('Create')))
        : h('div', { class: 'grid2', style: { gap: '14px' } },
          filtered.map(p => {
            const [fg, bg] = STATUS_COLOR[p.status] || STATUS_COLOR.ready;
            const imgId = p.candidateAssetId || p.sourceAssetId;
            return h('div', { style: { cursor: 'pointer' }, onClick: () => openProject(p) },
              h('div', { class: 'card-art' },
                imgId && h('img', { src: assetUrl(imgId), style: { position: 'absolute', inset: 0, width: '100%', height: '100%', objectFit: 'cover' }, loading: 'lazy', alt: p.styleName || t('Project') }),
                h('div', { style: { position: 'absolute', top: '8px', left: '8px', font: '600 9.5px var(--sans)', letterSpacing: '.4px', color: fg, background: bg, borderRadius: '999px', padding: '3px 8px' } }, t('status.' + p.status))),
              h('div', { style: { font: '600 13px var(--sans)', padding: '7px 2px 0' } }, p.styleName || p.title || t('Untitled')),
              h('div', { style: { font: '400 11px var(--sans)', color: 'var(--ink-muted)', padding: '2px 2px 0' } }, fmtDate(p.updatedAt)),
            );
          })),
    ),
  );
}

async function openProject(p) {
  if (!p.jobId) { toast(t('This project has no work yet')); return; }
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
    else toast(t('That attempt failed — try a new one'));
  } catch { toast(t('Could not open project')); }
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
  const planName = isFree ? t('Free account') : (S.products.find(p => p.internalKey === ent.plan)?.displayName || 'Creator');
  const rows = [
    isNative() && !isFree && [t('Manage subscription'), () => openPaywall('manage')],
    isNative() && [t('Restore purchases'), async () => { await refreshEnt(); render(); toast(S.ent.plan === 'free' ? t('No active plan found') : t('Purchases restored')); }],
    [t('Purchase history'), () => openInfo('purchases')],
    supportQQ() && [t('QQ group · buy credits') + ' · ' + supportQQ(), () => openPaywall('profile')],
    [t('Contact us'), () => { location.href = supportMailto(); }],
    [t('Privacy & data'), () => openInfo('privacy')],
    [t('About our styles'), () => openInfo('about')],
    [t('Language') + ' · ' + (getLang() === 'zh' ? 'English' : '中文'), toggleLang],
    signedIn() && [t('Sign out'), signOut],
    [t('Delete account'), () => openInfo('delete')],
  ].filter(Boolean);
  const name = userLabel() || (signedIn() ? t('Creator') : t('Guest'));
  return shell('profile',
    h('div', { class: 'scroll narrow', style: { padding: 'max(22px, env(safe-area-inset-top)) 20px 116px' } },
      h('div', { class: 'page-title', style: { padding: '14px 0 16px' } }, t('Profile')),
      h('div', { style: { paddingBottom: '18px' } },
        h('div', { style: { display: 'flex', alignItems: 'center', gap: '14px' } },
          h('div', { style: { width: '54px', height: '54px', borderRadius: '999px', background: 'var(--ink)', color: 'var(--canvas)', display: 'flex', alignItems: 'center', justifyContent: 'center', font: '600 22px var(--serif)' } }, (name[0] || 'G').toUpperCase()),
          h('div', { style: { minWidth: 0 } },
            h('div', { style: { font: '600 16px var(--sans)', overflow: 'hidden', textOverflow: 'ellipsis' } }, name),
            h('div', { style: { font: '400 12px var(--sans)', color: 'var(--ink-muted)' } },
              signedIn() ? t('Signed in — works and credits sync across devices') : t('Works are kept on this device only')))),
        !signedIn() && h('div', { style: { paddingTop: '14px' } },
          h('button', { class: 'btn', style: { height: '46px', fontSize: '14.5px' }, onClick: () => openAuth('profile') },
            t('Sign in / Register — {n} free artworks', { n: freeUnits() }))),
      ),
      h('div', { class: 'panel', style: { padding: '16px' } },
        h('div', { style: { display: 'flex', alignItems: 'center', justifyContent: 'space-between' } },
          h('div', { style: { font: '600 15px var(--serif)' } }, planName),
          h('span', { class: 'pillbtn', style: { cursor: 'default' } }, unitsBadgeText())),
        h('div', { style: { font: '400 12px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '4px' } },
          isFree
            ? (signedIn()
              ? t('{n} artworks are included with your account. Packs start at {price}; buy in our QQ group or by email.', { n: freeUnits(), price: (offeredProducts().filter(p => p.productType === 'pack').sort((a, b) => a.grantedUnits - b.grantedUnits)[0] && productPrice(offeredProducts().filter(p => p.productType === 'pack').sort((a, b) => a.grantedUnits - b.grantedUnits)[0])) || '—' })
              : t('Register with your email to receive {n} free artworks.', { n: freeUnits() }))
            : t('All directions unlocked · priority creation')),
        isFree && h('button', { class: 'btn', style: { marginTop: '12px', height: '42px', borderRadius: '10px', fontSize: '13.5px' }, onClick: () => signedIn() ? openPaywall('profile') : openAuth('profile') },
          signedIn() ? t('See packs & prices') : t('Register'))),
      h('div', { class: 'panel', style: { marginTop: '16px', overflow: 'hidden' } },
        rows.map(([label, onClick], i) => h('button', {
          style: {
            display: 'flex', alignItems: 'center', justifyContent: 'space-between', width: '100%', background: 'none', cursor: 'pointer',
            padding: '13px 16px', border: 'none', borderBottom: i < rows.length - 1 ? '1px solid rgba(217,213,204,.55)' : 'none',
            font: '500 13.5px var(--sans)', color: label === t('Delete account') ? 'var(--danger)' : 'var(--ink)', textAlign: 'left',
          }, onClick,
        }, label, svg(icons.chev)))),
      h('div', { style: { textAlign: 'center', font: '400 10.5px var(--mono)', letterSpacing: '1px', color: 'var(--ink-muted)', padding: '24px 0 8px' } }, 'MUSEFRAME 0.2'),
    ),
  );
}

// ---------- auth (email verification code = registration + sign-in) ----------
function openAuth(returnTo) {
  S.authReturn = returnTo || S.screen;
  S.paywall = null;
  S.emailLogin = { stage: 'email', email: S.emailLogin?.email || '', busy: false };
  track('auth_opened', { from: S.authReturn });
  go('auth');
}
function AuthScreen() {
  const cfg = S.authConfig || {};
  const emailOn = !!cfg.email?.enabled;
  const showGoogle = !!cfg.google?.enabled;
  const showApple = !!cfg.apple?.enabled && platform() === 'ios';
  const back = () => go(S.authReturn && S.authReturn !== 'auth' ? S.authReturn : 'discover');
  return shell(null,
    topbar(t('Sign in / Register'), back),
    h('div', { class: 'scroll narrow', style: { padding: '18px 20px 40px' } },
      h('div', { class: 'auth-card' },
        h('div', { class: 'kicker', style: { paddingBottom: '8px' } }, t('MuseFrame account')),
        h('div', { style: { font: '600 26px/1.2 var(--serif)' } }, t('Register with your email')),
        h('div', { style: { font: '400 13.5px/1.6 var(--sans)', color: 'var(--ink-muted)', padding: '8px 0 18px' } },
          t('{n} free artworks on sign-up. No password — we email you a 6-digit code. Signing in on a new device uses the same steps.', { n: freeUnits() })),
        emailOn
          ? EmailLoginBox(back)
          : h('div', { style: { font: '500 12.5px/1.55 var(--sans)', color: 'var(--warning)', background: 'var(--warning-soft)', borderRadius: '10px', padding: '10px 12px' } },
            t('Email sign-in is not enabled on this server yet. Please contact {email}.', { email: supportEmail() })),
        supportQQ() && h('div', { style: { font: '400 12px/1.55 var(--sans)', color: 'var(--ink-muted)', paddingTop: '14px' } }, t('Need help or more artworks? Join our QQ group {n}.', { n: supportQQ() })),
        (showGoogle || showApple) && [
          h('div', { style: { display: 'flex', alignItems: 'center', gap: '12px', padding: '18px 0 12px' } },
            h('div', { style: { flex: 1, height: '1px', background: 'var(--line)' } }),
            h('div', { style: { font: '400 11px var(--sans)', color: 'var(--ink-muted)' } }, t('or')),
            h('div', { style: { flex: 1, height: '1px', background: 'var(--line)' } })),
          h('div', { style: { display: 'flex', gap: '10px' } },
            showGoogle && h('button', { class: 'btn secondary small', style: { flex: 1 }, onClick: () => signIn('google', back) }, t('Continue with Google')),
            showApple && h('button', { class: 'btn secondary small', style: { flex: 1 }, onClick: () => signIn('apple', back) }, t('Continue with Apple'))),
        ],
        h('div', { style: { font: '400 11px/1.55 var(--sans)', color: 'var(--ink-muted)', paddingTop: '18px' } },
          t('Your address is used for sign-in codes and, if you ask for more artworks, to reply to you. Nothing else.'), ' ',
          h('button', { class: 'linkbtn', style: { fontSize: '11px', fontWeight: 500 }, onClick: () => openInfo('privacy') }, t('Privacy & data'))),
      ),
    ),
  );
}

function afterSignIn(res) {
  setToken(res.accessToken);
  S.user = res.user;
  localStorage.setItem('mf.userName', res.user.displayName || res.user.email || '');
  localStorage.setItem('mf.userEmail', res.user.email || '');
  localStorage.setItem('mf.signedIn', '1');
}
async function signIn(provider, done) {
  try {
    const res = await nativeSignIn(provider);
    afterSignIn(res);
    await refreshEnt();
    toast(t('Welcome, {name}', { name: res.user.displayName || t('Creator') }));
    done ? done() : render();
    renderOverlay();
  } catch (e) {
    toast(e.userMessage || t('Sign-in failed — please try again'));
  }
}
async function signOut() {
  clearToken();
  localStorage.removeItem('mf.signedIn');
  localStorage.removeItem('mf.userName');
  localStorage.removeItem('mf.userEmail');
  S.user = null; S.ent = null; S.projects = []; S.purchases = [];
  try { await ensureSession(await deviceId()); await loadCore(); } catch { /* shown on next render */ }
  toast(t('Signed out'));
  go('discover');
}

// Email verification-code login. `done` runs after a successful sign-in.
function EmailLoginBox(done) {
  const st = S.emailLogin || (S.emailLogin = { stage: 'email', email: '', busy: false });
  const input = (ph, val, oninput, extra) => h('input', {
    class: 'input', type: extra?.type || 'text', inputmode: extra?.inputmode, autocomplete: extra?.autocomplete, placeholder: ph, value: val,
    onInput: (e) => oninput(e.target.value),
    onKeyDown: (e) => { if (e.key === 'Enter') extra?.onEnter?.(); },
  });
  const wrap = (...kids) => h('div', { style: { paddingTop: '4px' } }, kids);
  const requestCode = async () => {
    const email = st.email.trim();
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) { toast(t('Please enter a valid email address')); return; }
    st.busy = true; render();
    try { await emailRequestCode(email); st.stage = 'code'; st.code = ''; toast(t('Code sent — check your inbox (and spam)')); }
    catch (e) {
      toast(e.code === 'PROVIDER_NOT_CONFIGURED' ? t('Email sign-in is not enabled yet')
        : e.code === 'RATE_LIMITED' ? t('Too many codes requested — wait a few minutes')
        : t('Could not send the code — try again later'));
    }
    finally { st.busy = false; render(); }
  };
  const verify = async () => {
    st.busy = true; render();
    try {
      const res = await emailVerifyCode(st.email.trim(), (st.code || '').trim());
      afterSignIn(res); S.emailLogin = null;
      await refreshEnt();
      const gained = S.ent?.availableUnits || 0;
      toast(gained > 0 ? t('Welcome — {n} free artworks are on your account', { n: gained }) : t('Signed in'), 2600);
      track('signin_completed', { provider: 'email' });
      done ? done() : render();
      renderOverlay();
    } catch (e) {
      toast(e.message || t('That code is not right')); st.busy = false; render();
    }
  };
  if (st.stage === 'email') {
    return wrap(
      input(t('Your email address'), st.email, (v) => { st.email = v; }, { type: 'email', inputmode: 'email', autocomplete: 'email', onEnter: requestCode }),
      h('button', { class: 'btn', disabled: st.busy, onClick: requestCode }, st.busy ? t('Sending…') : t('Send me a code')),
    );
  }
  return wrap(
    h('div', { style: { font: '400 12px var(--sans)', color: 'var(--ink-muted)', paddingBottom: '6px' } }, t('We sent a code to {email}', { email: st.email })),
    input(t('6-digit code'), st.code || '', (v) => { st.code = v; }, { inputmode: 'numeric', autocomplete: 'one-time-code', onEnter: verify }),
    h('button', { class: 'btn', disabled: st.busy, onClick: verify }, st.busy ? t('Checking…') : t('Continue')),
    h('div', { style: { display: 'flex', justifyContent: 'center', gap: '18px', paddingTop: '12px' } },
      h('button', { class: 'linkbtn', onClick: () => { S.emailLogin = { stage: 'email', email: st.email, busy: false }; render(); } }, t('Use another email')),
      h('button', { class: 'linkbtn', disabled: st.busy, onClick: requestCode }, t('Resend code'))),
  );
}

// ---------- paywall → "register" or "email us" ----------
function openPaywall(context) {
  S.paywall = { context };
  track('paywall_viewed', { context });
  renderOverlay();
}
function supportMailto(kind = 'more', product = null) {
  const who = userEmail() || userLabel() || '';
  if (kind === 'buy' && product) {
    const price = productPrice(product) || '';
    const subject = t('MuseFrame — buy {name} ({price}) for {email}', { name: productName(product), price, email: who || t('my account') });
    const body = t('Hello MuseFrame,\n\nI would like to buy {name} ({n} artworks, {price}) for my account ({email}).\nPlease tell me how to pay.\n\nThanks!', { name: productName(product), n: product.grantedUnits, price, email: who || '…' });
    return `mailto:${supportEmail()}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
  }
  const subject = kind === 'premium'
    ? t('MuseFrame — Premium directions for {email}', { email: who || t('my account') })
    : t('MuseFrame — more artworks for {email}', { email: who || t('my account') });
  const body = kind === 'premium'
    ? t('Hello MuseFrame,\n\nI would like Premium directions enabled on my account ({email}).\n\nThanks!', { email: who || '…' })
    : t('Hello MuseFrame,\n\nI have used my free artworks and would like more on my account ({email}).\nHow many I would like: \n\nThanks!', { email: who || '…' });
  return `mailto:${supportEmail()}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
}
async function copySupportEmail() {
  try { await navigator.clipboard.writeText(supportEmail()); toast(t('Address copied')); }
  catch { toast(supportEmail(), 3000); }
}
async function copyQQ() {
  try { await navigator.clipboard.writeText(supportQQ()); toast(t('Group number copied')); }
  catch { toast(supportQQ(), 3000); }
}
// QQ group first (fastest way to buy), email second. QR only where there is room.
function ContactBlock(kind = 'more') {
  const qq = supportQQ();
  return h('div', { class: 'contact-card', style: { marginTop: '4px' } },
    qq && h('div', { class: 'qq-row' },
      isWeb() && h('img', { class: 'qq-qr', src: apiUrl('/qq-group.png'), alt: t('QQ group QR code'), width: 96, height: 96 }),
      h('div', { style: { flex: 1, minWidth: 0 } },
        h('div', { class: 'kicker', style: { paddingBottom: '4px' } }, t('QQ group · fastest')),
        h('div', { class: 'email' }, qq),
        h('div', { style: { font: '400 11.5px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '4px' } }, t('Join the group and message the admin to buy — credited within minutes during the day.')),
        h('div', { style: { display: 'flex', gap: '8px', paddingTop: '10px' } },
          h('button', { class: 'btn small', style: { flex: 'none', width: 'auto', padding: '0 14px', height: '40px' }, onClick: copyQQ }, t('Copy group number')),
          !isWeb() && h('a', { class: 'btn secondary small', style: { flex: 'none', width: 'auto', padding: '0 14px', height: '40px', textDecoration: 'none' }, href: apiUrl('/qq-group.png'), target: '_blank', rel: 'noopener' }, t('QR code'))))),
    h('div', { style: { paddingTop: qq ? '14px' : 0, marginTop: qq ? '12px' : 0, borderTop: qq ? '1px solid var(--line)' : 'none' } },
      h('div', { class: 'kicker', style: { paddingBottom: '4px' } }, qq ? t('Or email us') : t('Email us')),
      h('div', { class: 'email', style: { fontSize: '13.5px' } }, supportEmail()),
      userEmail() && h('div', { style: { font: '400 11.5px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '4px' } }, t('Mention the address you registered with: {email}', { email: userEmail() })),
      h('div', { style: { display: 'flex', gap: '8px', paddingTop: '10px' } },
        h('a', { class: 'btn secondary small', style: { flex: 'none', width: 'auto', padding: '0 14px', height: '40px', textDecoration: 'none' }, href: supportMailto(kind) }, svg(icons.mail), t('Write to us')),
        h('button', { class: 'btn secondary small', style: { flex: 'none', width: 'auto', padding: '0 14px', height: '40px' }, onClick: copySupportEmail }, t('Copy address')))),
  );
}
function PaywallSheet() {
  const ctx = S.paywall.context;
  const close = () => { S.paywall = null; renderOverlay(); };
  const guest = !signedIn();
  const premium = ctx === 'premium';
  const n = freeUnits();

  let title, body;
  if (guest && freeNeedsAuth()) {
    title = t('Register to get {n} free artworks', { n });
    body = t('Sign up with your email — no password, no card. {n} complete artworks are on us. If you already have an account, the same steps sign you in.', { n });
  } else if (premium) {
    title = t('Premium direction');
    body = t('Premium directions come with Creator. Pick the plan below, then join our QQ group or email us — we enable it on your account.');
  } else if ((S.ent?.availableUnits || 0) > 0) {
    title = t('Your artworks');
    body = t('You have {n} left. Packs below add more — buy in our QQ group or by email and we credit your account.', { n: S.ent.availableUnits });
  } else {
    title = t('Your free artworks are used up');
    body = t('You have used the {n} free artworks that come with your account. Pick a pack below, then join our QQ group or email us — we credit your account by hand.', { n });
  }

  const contactCard = ContactBlock(premium ? 'premium' : 'more');

  return h('div', { class: 'sheet-backdrop', onClick: close },
    h('div', { class: 'sheet', onClick: (e) => e.stopPropagation() },
      h('div', { class: 'sheet-grab' }),
      h('div', { style: { display: 'flex', alignItems: 'center', gap: '12px', paddingBottom: '8px' } },
        h('div', { style: { width: '44px', height: '55px', borderRadius: '8px', flex: 'none', background: artBg(S.draft.style) || 'linear-gradient(135deg,#1C49D8,#0A1C52)', backgroundSize: 'cover' } }),
        h('div', { style: { flex: 1, font: '600 19px/1.25 var(--serif)' } }, title),
        h('button', { class: 'iconbtn', style: { width: '30px', height: '30px', background: 'var(--canvas)', color: 'var(--ink-muted)', fontSize: '13px' }, onClick: close, 'aria-label': t('Close') }, '✕')),
      h('div', { style: { font: '400 13px/1.55 var(--sans)', color: 'var(--ink-muted)', paddingBottom: '16px' } }, body),
      guest && freeNeedsAuth()
        ? [
          h('button', { class: 'btn', onClick: () => openAuth(S.screen) }, t('Register with email')),
          h('div', { style: { textAlign: 'center', font: '400 11.5px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '12px' } },
            t('Questions? '), supportQQ() && [t('QQ group '), h('b', null, supportQQ()), ' · '], h('a', { class: 'linkbtn', style: { textDecoration: 'none', fontWeight: 500 }, href: `mailto:${supportEmail()}` }, supportEmail())),
        ]
        : [
          Catalogue(premium),
          contactCard,
          nativeBilling() && StorePlans(close),
          h('div', { style: { textAlign: 'center', font: '400 10.5px/1.5 var(--sans)', color: 'var(--ink-muted)', paddingTop: '14px' } },
            t('Failed or rejected generations never use an artwork.')),
        ],
    ),
  );
}

// Price list (email-to-buy). Packs first, then the Creator plan; the Premium
// context leads with Creator since packs alone do not unlock those directions.
function Catalogue(premiumFirst) {
  let items = offeredProducts();
  if (!items.length) return null;
  const packs = items.filter(p => p.productType === 'pack').sort((a, b) => a.grantedUnits - b.grantedUnits);
  const subs = items.filter(p => p.productType === 'subscription');
  items = premiumFirst ? [...subs, ...packs] : [...packs, ...subs];
  return h('div', { style: { display: 'flex', flexDirection: 'column', gap: '8px', paddingBottom: '14px' } },
    items.map(p => {
      const sub = p.productType === 'subscription';
      return h('a', { class: 'plan-row' + (sub ? ' selected' : ''), href: supportMailto('buy', p), style: { textDecoration: 'none', color: 'inherit' } },
        h('div', { style: { flex: 1, minWidth: 0 } },
          h('div', { style: { font: '600 14px var(--sans)' } }, productName(p), sub && h('span', { style: { font: '600 9.5px var(--sans)', letterSpacing: '.5px', color: 'var(--cobalt)', marginLeft: '8px' } }, t('PREMIUM + HIGH-RES'))),
          h('div', { style: { font: '400 11.5px var(--sans)', color: 'var(--ink-muted)' } },
            sub ? t('{n} artworks every {period} · all directions · high tier', { n: p.grantedUnits, period: t('period.' + p.period) })
              : t('{n} artworks · {each} each · never expire', { n: p.grantedUnits, each: perImage(p) }))),
        h('div', { style: { textAlign: 'right', flex: 'none' } },
          h('div', { style: { font: '600 15px var(--sans)' } }, productPrice(p), sub && h('span', { style: { font: '400 11px var(--sans)', color: 'var(--ink-muted)' } }, ' / ' + t('period.' + p.period))),
          h('div', { style: { font: '600 11px var(--sans)', color: 'var(--cobalt)' } }, supportQQ() ? t('QQ / email to buy') : t('Email to buy'))));
    }));
}

// Store purchases (Android/iOS only, and only once billing is configured
// server-side). Prices come from the server catalogue; the web never shows them.
function StorePlans(close) {
  const plans = S.products.filter(p => p.productType === 'subscription');
  const mini = S.products.filter(p => p.productType === 'pack').sort((a, b) => a.grantedUnits - b.grantedUnits)[0];
  const sel = plans.find(p => p.internalKey === S.payPlan) || plans[0];
  const price = (m, cur, p) => (p && productPrice(p)) || `${cur === 'CNY' ? '¥' : '$'}${(m / 100).toFixed(2)}`;
  const buy = async (key) => {
    const product = S.products.find(p => p.internalKey === key);
    try {
      const native = await nativePurchase(product);
      if (!native) { toast(t('Store purchases are only available in the app')); return; }
      const res = await post('/v1/purchases/verify', { ...native, productKey: key });
      S.ent = res.entitlements; close();
      render();
      toast(product.productType === 'pack' ? t('Pack added — {n} artworks', { n: product.grantedUnits }) : t('Welcome to Creator'));
      track('purchase_completed', { productId: key });
    } catch (e) {
      if (e.userMessage) toast(e.userMessage);
      else if (e.code === 'PROVIDER_NOT_CONFIGURED') toast(t('Store purchases are not open yet'));
      else if (e.code === 'PURCHASE_INVALID') toast(t('Purchase could not be verified — contact us if you were charged'));
      else toast(t('Purchase failed — you were not charged'));
    }
  };
  if (!plans.length && !mini) return null;
  return h('div', { style: { paddingTop: '18px' } },
    h('div', { class: 'kicker', style: { paddingBottom: '10px' } }, t('Or buy in the store')),
    h('div', { style: { display: 'flex', flexDirection: 'column', gap: '10px' } },
      plans.map(p => h('div', {
        class: 'plan-row' + (S.payPlan === p.internalKey ? ' selected' : ''),
        onClick: () => { S.payPlan = p.internalKey; renderOverlay(); },
      },
        h('div', { class: 'radio' }),
        h('div', { style: { flex: 1 } },
          h('div', { style: { font: '600 14px var(--sans)' } }, productName(p)),
          h('div', { style: { font: '400 11.5px var(--sans)', color: 'var(--ink-muted)' } }, t('{n} artworks / {period} · all directions', { n: p.grantedUnits, period: t('period.' + p.period) }))),
        h('div', { style: { font: '600 14px var(--sans)' } }, price(p.priceMinor, p.currency, p))))),
    sel && h('button', { class: 'btn', style: { marginTop: '14px', height: '50px', fontSize: '15px' }, onClick: () => buy(sel.internalKey) },
      t('Continue — {price} / {period}', { price: price(sel.priceMinor, sel.currency, sel), period: t('period.' + sel.period) })),
    mini && h('button', { class: 'linkbtn', style: { width: '100%', textAlign: 'center', paddingTop: '14px', color: 'var(--ink)', fontSize: '12.5px' }, onClick: () => buy(mini.internalKey) },
      t('Not ready? {name} — {n} artworks for {price}', { name: productName(mini), n: mini.grantedUnits, price: price(mini.priceMinor, mini.currency, mini) })),
    h('button', { class: 'linkbtn', style: { width: '100%', textAlign: 'center', paddingTop: '10px', fontWeight: 500, fontSize: '12px' }, onClick: async () => { await refreshEnt(); render(); toast(S.ent.plan === 'free' ? t('No previous purchases found') : t('Purchases restored')); } }, t('Restore purchases')),
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
          h('div', { class: 'card-tags', style: { paddingTop: '8px' } }, t('Works best with — ') + c.suitabilityTags.map(tagLabel).join(' · ')),
          c.premium && h('div', { style: { paddingTop: '8px' } }, h('span', { style: { font: '600 9px var(--sans)', letterSpacing: '.6px', background: 'var(--canvas)', border: '1px solid var(--line)', borderRadius: '999px', padding: '3px 7px' } }, 'PREMIUM')))),
      h('div', { style: { font: '400 12.5px/1.6 var(--sans)', color: 'var(--ink-muted)', padding: '14px 0' } },
        t('An original MuseFrame direction. Identity, pose and key objects are preserved; light, color and texture follow the direction. May change: background detail, fine texture.')),
      locked && h('div', { style: { font: '500 11.5px/1.5 var(--sans)', color: 'var(--warning)', background: 'var(--warning-soft)', borderRadius: '10px', padding: '9px 12px', marginBottom: '12px' } },
        t('Premium direction — opened on request.')),
      h('button', {
        class: 'btn', onClick: () => {
          S.draft.style = c; S.detail = null; renderOverlay();
          S.draft.assetId ? go('configure') : startImport(S.screen);
        },
      }, t('Use this direction')),
    ),
  );
}

const INFO = () => ({
  privacy: [t('Privacy & data'), t('Your photos are used only to create the results you request. Uploads are re-encoded on your device, which removes location and camera metadata. Source photos are kept for 30 days by default; results stay until you delete them. Deleting a project removes its images from storage.')],
  about: [t('About our styles'), t('All directions are original MuseFrame StyleSpecs, organized in curated exhibitions. Each is a versioned, tested product asset built from public-domain visual principles — no third-party prompt packs, no living artists’ names. Published versions are immutable: your old works always re-render the version they used.')],
  delete: [t('Delete account'), t('To delete your account and its images, email us from the address you registered with. Sign-in is revoked, images enter a purge queue, and legally required payment records are separated and de-identified. To only clear this device, sign out.')],
  purchases: null, // rendered dynamically
});
function openInfo(key) { S.infoSheet = key; renderOverlay(); }
function InfoSheet() {
  const key = S.infoSheet;
  let title, body;
  if (key === 'purchases') {
    title = t('Purchase history');
    body = S.purchases.length
      ? h('div', null, S.purchases.map(p => h('div', { style: { display: 'flex', justifyContent: 'space-between', padding: '8px 0', borderBottom: '1px solid rgba(217,213,204,.5)', font: '400 13px var(--sans)' } },
        h('span', null, p.product), h('span', { style: { color: 'var(--ink-muted)' } }, `$${(p.amountMinor / 100).toFixed(2)} · ${fmtDate(p.purchasedAt, undefined)}`))))
      : t('No purchases yet. Artworks added by our team after you email us are shown in your balance, not here.');
  } else [title, body] = INFO()[key];
  return h('div', { class: 'sheet-backdrop', onClick: () => { S.infoSheet = null; renderOverlay(); } },
    h('div', { class: 'sheet', onClick: (e) => e.stopPropagation() },
      h('div', { class: 'sheet-grab' }),
      h('div', { style: { font: '600 20px var(--serif)', paddingBottom: '10px' } }, title),
      h('div', { style: { font: '400 13px/1.65 var(--sans)', color: 'var(--ink-muted)' } }, body),
      key === 'delete' && h('a', { class: 'btn secondary', style: { marginTop: '14px', textDecoration: 'none' }, href: `mailto:${supportEmail()}?subject=${encodeURIComponent(t('MuseFrame — delete my account ({email})', { email: userEmail() || '' }))}` }, t('Email us to delete')),
      h('button', { class: 'btn secondary', style: { marginTop: '18px' }, onClick: () => { S.infoSheet = null; renderOverlay(); } }, t('Close')),
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
  auth: AuthScreen,
};

function render() {
  document.documentElement.lang = getLang() === 'zh' ? 'zh-CN' : 'en';
  document.title = getLang() === 'zh' ? 'MuseFrame — 策展式 AI 艺术照' : 'MuseFrame — Curated image making';
  app.replaceChildren(SCREENS[S.screen]?.() || DiscoverScreen());
  renderOverlay();
}
function renderOverlay() {
  const parts = [];
  if (S.analyzing) parts.push(h('div', { style: { position: isWeb() ? 'fixed' : 'absolute', inset: 0, background: 'rgba(247,245,239,.92)', display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: '14px', backdropFilter: 'blur(6px)', zIndex: 70 } },
    h('div', { class: 'spinner' }),
    h('div', { style: { font: '600 20px var(--serif)' } }, t('Reading the image')),
    h('div', { style: { font: '400 12.5px var(--sans)', color: 'var(--ink-muted)' } }, t('Subject, light and composition — a moment.'))));
  if (S.detail) parts.push(StyleDetailSheet());
  if (S.infoSheet) parts.push(InfoSheet());
  if (S.paywall) parts.push(PaywallSheet());
  if (S.toast) parts.push(h('div', { class: 'toast' }, S.toast));
  overlayRoot.replaceChildren(...parts);
}

// ---------- deep links (?exhibition=slug, ?plan=…, ?auth=1) ----------
function applyDeepLinks() {
  const slug = params.get('exhibition');
  if (slug && allShelves().some(s => s.slug === slug)) {
    S.exhibition = slug;
    if (S.screen === 'onboarding') localStorage.setItem('mf.onboarded', '1');
    S.screen = 'exhibition';
    track('exhibition_opened', { slug, source: 'link' });
  }
  if (params.get('plan')) {
    if (S.screen === 'onboarding') S.screen = 'discover';
    S.paywall = { context: 'link:' + params.get('plan') };
  }
  if (params.get('auth') === '1' && !signedIn()) {
    if (S.screen === 'onboarding') S.screen = 'discover';
    S.authReturn = S.screen; S.screen = 'auth';
  }
}

// ---------- boot ----------
async function bootApp() {
  S.bootError = false;
  render(); // paint loader immediately
  try {
    const session = await ensureSession(await deviceId());
    if (session === 'guest' && signedIn()) {
      // The stored token expired and a guest session replaced it — the local
      // "signed in" flag would otherwise promise a balance that is not there.
      localStorage.removeItem('mf.signedIn'); localStorage.removeItem('mf.userName'); localStorage.removeItem('mf.userEmail');
    }
    S.authConfig = await getAuthConfig();
    await loadCore();
    applyDeepLinks();
    track('discover_viewed', { layout: S.layout, lang: getLang() });
  } catch (e) {
    S.bootError = true;
  }
  S.booted = true;
  render();
}
initLang(params.get('lang'));
bootApp();
