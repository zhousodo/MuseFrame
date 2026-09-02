// Email delivery via SMTP (configurable in admin → 配置). Provider-agnostic:
// swap host/user/pass in the config panel to change email services, no redeploy.
import nodemailer from 'nodemailer';
import { cfg } from './configStore.js';

let cached = null; // { key, transport }

function configKey() {
  return [cfg('smtp_host'), cfg('smtp_port'), cfg('smtp_user'), cfg('smtp_pass')].join('|');
}

export function smtpConfigured() {
  return !!(cfg('smtp_host') && cfg('smtp_user') && cfg('smtp_pass'));
}

// Rebuilds the transport whenever the live SMTP config changes.
function transport() {
  if (!smtpConfigured()) { const e = new Error('SMTP not configured'); e.code = 'SMTP_NOT_CONFIGURED'; throw e; }
  const key = configKey();
  if (!cached || cached.key !== key) {
    const port = Number(cfg('smtp_port')) || 587;
    cached = {
      key,
      transport: nodemailer.createTransport({
        host: cfg('smtp_host'),
        port,
        secure: port === 465, // 465 = implicit TLS; 587 = STARTTLS
        auth: { user: cfg('smtp_user'), pass: cfg('smtp_pass') },
        // Without these, nodemailer inherits the OS TCP timeout: a black-holed
        // SMTP host (provider outage, firewall change, wrong port) held
        // /v1/auth/email/request open for two to ten minutes per call, with the
        // user staring at a spinner and the request still occupying the process.
        connectionTimeout: Number(process.env.SMTP_CONNECTION_TIMEOUT_MS) || 10_000,
        greetingTimeout: 10_000,
        socketTimeout: 20_000,
        dnsTimeout: 5_000,
      }),
    };
  }
  return cached.transport;
}

export async function sendMail({ to, subject, text, html }) {
  const info = await transport().sendMail({ from: cfg('smtp_from'), to, subject, text, html });
  return { messageId: info.messageId, accepted: info.accepted };
}

export async function sendLoginCode(to, code) {
  const subject = `${code} 是你的 MuseFrame 登录验证码`;
  const text = `你的 MuseFrame 登录验证码是：${code}\n\n验证码 10 分钟内有效。如果不是你本人操作，请忽略此邮件。`;
  const html = `<div style="font-family:-apple-system,'PingFang SC',sans-serif;max-width:420px;margin:0 auto;padding:24px">
    <div style="font:600 22px Georgia,serif;letter-spacing:2px;color:#171717">MUSEFRAME</div>
    <p style="color:#6E6B66;font-size:14px">你的登录验证码：</p>
    <div style="font:700 34px ui-monospace,monospace;letter-spacing:8px;color:#1C49D8;padding:8px 0">${code}</div>
    <p style="color:#6E6B66;font-size:12px">10 分钟内有效。如果不是你本人操作，请忽略此邮件。</p>
  </div>`;
  return sendMail({ to, subject, text, html });
}
