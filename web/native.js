// Native bridge: sign-in, purchases, device identity. In the packaged app these
// use Capacitor plugins (auto-registered on window.Capacitor.Plugins — no
// bundler needed); in the browser they degrade gracefully.
import { post, apiUrl } from './api.js';

const P = () => window.Capacitor?.Plugins || {};
export const isNative = () => !!window.Capacitor?.isNativePlatform?.();
export const platform = () => window.Capacitor?.getPlatform?.() || 'web';

// ---- stable device id (anti free-grant farming) ----
let cachedDeviceId = null;
export async function deviceId() {
  if (cachedDeviceId) return cachedDeviceId;
  try {
    const d = await P().Device?.getId?.();
    if (d?.identifier) return (cachedDeviceId = d.identifier);
  } catch { /* fall through */ }
  let v = localStorage.getItem('mf.device');
  if (!v) { v = crypto.randomUUID(); localStorage.setItem('mf.device', v); }
  return (cachedDeviceId = v);
}

// ---- auth config (served by backend; enables providers without app updates) ----
let authConfig = null;
export async function getAuthConfig() {
  if (authConfig) return authConfig;
  try { authConfig = await fetch(apiUrl('/v1/auth/config')).then((r) => r.json()); }
  catch { authConfig = { guestAllowed: true, google: { enabled: false }, apple: { enabled: false }, billing: {} }; }
  return authConfig;
}

// ---- social sign-in ----
let slReady = false;
async function initSocial(cfg) {
  const SL = P().SocialLogin;
  if (!SL || slReady) return;
  const init = {};
  if (cfg.google?.enabled && cfg.google.webClientId) init.google = { webClientId: cfg.google.webClientId };
  if (cfg.apple?.enabled) init.apple = {};
  await SL.initialize(init);
  slReady = true;
}

/**
 * Sign in with 'google' | 'apple'. Returns the server session
 * {accessToken, user} after exchanging the verified ID token. Throws with a
 * user-presentable .userMessage on failure.
 */
export async function nativeSignIn(provider) {
  const cfg = await getAuthConfig();
  if (!cfg[provider]?.enabled) {
    const e = new Error('provider disabled'); e.userMessage = '该登录方式暂未开通'; throw e;
  }
  const SL = P().SocialLogin;
  if (!SL) { const e = new Error('no plugin'); e.userMessage = '请在手机 App 内使用第三方登录'; throw e; }
  await initSocial(cfg);
  let res;
  try {
    res = await SL.login({ provider, options: { scopes: ['email', 'profile'] } });
  } catch (err) {
    const e = new Error('login cancelled'); e.userMessage = '登录已取消'; throw e;
  }
  const r = res?.result || {};
  const idToken = r.idToken || r.identityToken || r.accessToken?.token;
  if (!idToken) { const e = new Error('no idToken'); e.userMessage = '未获取到登录凭证'; throw e; }
  // Exchange with current session attached: an in-flight guest account (its
  // projects, purchased units) merges into the real identity server-side.
  return await post('/v1/auth/exchange', {
    provider, identityToken: idToken, deviceId: await deviceId(),
    displayName: r.profile?.name || r.profile?.givenName || null,
    locale: navigator.language,
  });
}

// ---- in-app purchases ----
/**
 * Buy a product through the store. Returns the token/receipt payload to send
 * to /v1/purchases/verify, or null when native billing is unavailable.
 */
export async function nativePurchase(product) {
  const NP = P().NativePurchases;
  if (!NP || !isNative()) return null;
  const plat = platform(); // 'android' | 'ios'
  const productIdentifier = plat === 'ios' ? product.appleProductId : product.googleProductId;
  const opts = {
    productIdentifier,
    productType: product.productType === 'subscription' ? 'subs' : 'inapp',
    quantity: 1,
  };
  let r;
  try { r = await NP.purchaseProduct(opts); }
  catch (err) {
    const cancelled = /cancel/i.test(String(err?.message || err));
    const e = new Error('purchase failed');
    e.userMessage = cancelled ? '购买已取消' : '购买未完成，未扣款';
    throw e;
  }
  return {
    platform: plat === 'ios' ? 'apple' : 'google',
    purchaseToken: r?.purchaseToken || r?.receipt || null,
    transactionId: r?.transactionId || r?.transactionIdentifier || null,
  };
}
