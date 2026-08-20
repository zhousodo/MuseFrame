// Deployment configuration. In the packaged app (Capacitor) the UI is loaded
// from the device, so API calls need an absolute base — point this at the
// production backend. When the web app is served by the backend itself this
// value is ignored and same-origin relative URLs are used.
window.MF_CONFIG = {
  // 正式域名：Cloudflare 橙云 → Caddy(tls internal) → 127.0.0.1:8787
  apiBase: 'https://museframe.lenscript.cn',
};
