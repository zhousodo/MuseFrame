// Deployment configuration. In the packaged app (Capacitor) the UI is loaded
// from the device, so API calls need an absolute base — point this at the
// production backend. When the web app is served by the backend itself this
// value is ignored and same-origin relative URLs are used.
window.MF_CONFIG = {
  // nip.io 魔法域名 → 43.155.234.117（Caddy 反代 127.0.0.1:8787）。
  // Cloudflare 加好 museframe.lenscript.cn 记录后改为 https://museframe.lenscript.cn 并重打包。
  apiBase: 'http://museframe.43.155.234.117.nip.io',
};
