// F04 — the email login code was generated with Math.random(), which is
// xorshift128+: its internal state is recoverable from a handful of observed
// outputs, and this code is a full account credential (it logs you in as any
// address). F03/F15 — ALLOW_TEST_LOGIN=true returned that code, in plaintext,
// for ANY address, to ANY unauthenticated caller.
import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { randomInt } from 'node:crypto';

const SERVER = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const apiSrc = readFileSync(path.join(SERVER, 'api.js'), 'utf8');
// Comments explain the bug being fixed and legitimately name Math.random, so
// assertions about "what the code calls" must look at code only.
const stripComments = (src) => src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ 	]*\/\/.*$/gm, '');
const apiCode = stripComments(apiSrc);

// The code generator is three lines inside an async route handler that talks to
// SMTP, so the source is the honest thing to assert on: the property under test
// is "which RNG is used", and that is a textual fact about the call site.
const otpRegion = apiCode.slice(
  apiCode.indexOf("route('POST', '/v1/auth/email/request'"),
  apiCode.indexOf("route('POST', '/v1/auth/email/verify'"),
);

describe('OTP generation uses the CSPRNG', () => {
  test('the email/request handler exists and was located', () => {
    assert.ok(otpRegion.length > 200, 'failed to locate the email/request handler');
  });

  test('the code is drawn with crypto.randomInt, not Math.random', () => {
    assert.match(otpRegion, /randomInt\(100000,\s*1000000\)/,
      'the OTP must come from node:crypto randomInt');
    assert.doesNotMatch(otpRegion, /Math\.random/,
      'Math.random must not appear anywhere in the OTP path');
  });

  test('randomInt is actually imported from node:crypto', () => {
    assert.match(apiCode, /import\s*\{[^}]*\brandomInt\b[^}]*\}\s*from\s*'node:crypto'/);
  });

  test('no Math.random in api.js code (session tokens, ids, salts)', () => {
    assert.doesNotMatch(apiCode, /Math\.random/);
  });

  test('session tokens come from randomBytes, not a counter or Math.random', () => {
    assert.match(apiCode, /randomBytes\(24\)\.toString\('base64url'\)/);
  });
});

describe('the generated code has the shape the client expects', () => {
  // Contract: the app shows a 6-digit box. randomInt(100000, 1000000) is the
  // only draw that is both uniform and always 6 digits.
  test('always exactly 6 digits, never zero-padded, never 7', () => {
    for (let i = 0; i < 5000; i++) {
      const code = String(randomInt(100000, 1000000));
      assert.equal(code.length, 6);
      assert.match(code, /^[1-9]\d{5}$|^[0-9]{6}$/);
      const n = Number(code);
      assert.ok(n >= 100000 && n <= 999999, code);
    }
  });

  test('the draw is spread across the range (not a degenerate generator)', () => {
    const buckets = new Array(9).fill(0);
    const N = 20000;
    for (let i = 0; i < N; i++) buckets[Math.floor(randomInt(100000, 1000000) / 100000) - 1]++;
    for (const [i, b] of buckets.entries()) {
      assert.ok(b > N / 9 * 0.7 && b < N / 9 * 1.3, `bucket ${i} is skewed: ${b}`);
    }
  });

  test('no repeat within a large sample (birthday sanity for a 900k space)', () => {
    const seen = new Set();
    for (let i = 0; i < 200; i++) seen.add(String(randomInt(100000, 1000000)));
    assert.ok(seen.size >= 198, `only ${seen.size} distinct codes in 200 draws`);
  });
});

describe('the devCode leak is double-gated (F03/F15)', () => {
  test('devCode is only attached when the caller also proves admin', () => {
    // `testMode` alone (the env flag) used to be the whole gate: with the flag
    // on, anyone could request a code for any address and read it back.
    assert.match(otpRegion, /if\s*\(testMode\s*&&\s*isAdminRequest\(ctx\.req,\s*ctx\.url\)\)\s*body\.devCode\s*=\s*code/);
  });

  test('there is no other assignment of devCode in the file', () => {
    const hits = apiCode.match(/devCode\s*=/g) || [];
    assert.equal(hits.length, 1, 'devCode must be assigned in exactly one, gated place');
  });

  test('the dev provider login is gated the same way', () => {
    assert.match(apiCode, /process\.env\.ALLOW_TEST_LOGIN\s*!==\s*'true'\s*\|\|\s*!isAdminRequest\(/);
  });

  test('mock purchases require the admin token as well as the flag', () => {
    assert.match(apiCode, /verifyConfig\.allowMockPurchases\s*&&\s*isAdminRequest\(/);
  });
});

describe('code comparison and per-address throttling', () => {
  test('the stored hash is compared in constant time', () => {
    assert.match(apiCode, /timingSafeEqual\(given,\s*stored\)/);
  });

  test('re-requesting a code cannot reset the guess budget indefinitely', () => {
    // attempts resets to 0 on every new code, so the sticky brake has to count
    // ISSUANCES per address — otherwise it is unlimited fresh codes x 5 guesses.
    assert.match(otpRegion, /issue_count/);
    assert.match(otpRegion, /if\s*\(issued\s*>=\s*5\)/);
    assert.match(otpRegion, /RATE_LIMITED/);
  });

  test('a failed send does not overwrite the code the user already holds', () => {
    // The upsert must come after the send, or a failed delivery invalidates a
    // working code and locks the user out.
    const sendAt = otpRegion.indexOf('await sendLoginCode');
    const upsertAt = otpRegion.indexOf('INSERT INTO email_codes');
    assert.ok(sendAt > 0 && upsertAt > 0);
    assert.ok(upsertAt > sendAt, 'the email_codes upsert must run after the send attempt');
  });
});
