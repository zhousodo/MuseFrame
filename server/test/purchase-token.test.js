// F01 (critical) — Google Play purchase replay via URL-equivalent token
// variants. verify.js built the Play API URL by string interpolation and api.js
// deduped `purchases` on the RAW client token, so `<token>?x=1`, `<token>#frag`,
// `%2E`-escaped variants and a trailing `/` all resolved at Google to the SAME
// purchase while landing in our table as brand-new transactions: one payment,
// unlimited grants of paid units.
import { test, describe, before } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

process.env.MUSEFRAME_DATA_DIR = mkdtempSync(path.join(tmpdir(), 'mf-purchase-'));

const { assertPlayToken, PLAY_TOKEN_RE } = await import('../verify.js');

describe('assertPlayToken — the replay gate', () => {
  const GOOD = 'abcdefghijklmnop.AO-J1Ox_9Zk-1234567890abcdefgh-ABCDEFGH';

  test('accepts a real-shaped Play token', () => {
    assert.doesNotThrow(() => assertPlayToken(GOOD, 'creator_monthly'));
    assert.ok(PLAY_TOKEN_RE.test(GOOD));
  });

  // Each of these resolves at Google to the SAME purchase as GOOD once it is
  // interpolated into the URL path, but differs as a database key.
  const variants = {
    'query suffix': GOOD + '?x=1',
    'fragment': GOOD + '#',
    'fragment with value': GOOD + '#anything',
    'trailing slash': GOOD + '/',
    'percent-encoded dot': GOOD.replace('.', '%2E'),
    'percent-encoded slash': GOOD + '%2F',
    'path traversal': GOOD + '/../' + GOOD,
    'leading slash': '/' + GOOD,
    'trailing space': GOOD + ' ',
    'newline': GOOD + '\n',
    'tab': GOOD + '\t',
    'double query': GOOD + '?a=1&b=2',
    'semicolon param': GOOD + ';x=1',
    'unicode lookalike dot': GOOD + '。',
  };
  for (const [name, variant] of Object.entries(variants)) {
    test(`rejects the URL-equivalent variant: ${name}`, () => {
      assert.notEqual(variant, GOOD, 'variant must differ from the canonical token');
      assert.throws(() => assertPlayToken(variant, 'creator_monthly'),
        (e) => e.code === 'PURCHASE_TOKEN_INVALID', name);
    });
  }

  test('rejects a non-string token', () => {
    for (const bad of [null, undefined, 42, {}, [], true]) {
      assert.throws(() => assertPlayToken(bad, 'creator_monthly'), (e) => e.code === 'PURCHASE_TOKEN_INVALID');
    }
  });

  test('rejects an empty token', () => {
    assert.throws(() => assertPlayToken('', 'creator_monthly'), (e) => e.code === 'PURCHASE_TOKEN_INVALID');
  });

  test('rejects an absurdly long token (URL / storage bound)', () => {
    assert.throws(() => assertPlayToken('a'.repeat(513), 'creator_monthly'), (e) => e.code === 'PURCHASE_TOKEN_INVALID');
  });

  test('the product id is interpolated too, so it is validated too', () => {
    assert.throws(() => assertPlayToken(GOOD, 'creator/../../other'), (e) => e.code === 'PURCHASE_TOKEN_INVALID');
    assert.throws(() => assertPlayToken(GOOD, 'creator?x=1'), (e) => e.code === 'PURCHASE_TOKEN_INVALID');
  });

  test('every accepted token survives encodeURIComponent unchanged', () => {
    // This is the invariant that makes "what we store" and "what Google reads"
    // the same bytes: if the alphabet ever widens, this test fails loudly.
    for (const t of [GOOD, 'a', 'A.B_C-D.0', '_-.'.repeat(10)]) {
      assert.doesNotThrow(() => assertPlayToken(t, 'p'));
      assert.equal(encodeURIComponent(t), t, t);
    }
  });
});

describe('adoptCanonicalTxId — dedupe on the order id Google issued', () => {
  let api, db;
  before(async () => {
    db = await import('../db.js');
    api = await import('../api.js');
  });

  const mkUser = () => {
    const id = db.uuid();
    db.run('INSERT INTO users (id, display_name, is_guest, locale, status, created_at, updated_at) VALUES (?,?,?,?,?,?,?)',
      id, 'buyer', 0, 'en', 'active', db.now(), db.now());
    return id;
  };
  const mkProduct = () => {
    const id = db.uuid();
    db.run(`INSERT INTO products (id, internal_key, product_type, display_name, granted_units, price_minor, currency, active)
            VALUES (?,?,?,?,?,?,?,1)`, id, 'pack_' + id.slice(0, 6), 'pack', 'Test Pack', 8, 499, 'USD');
    return db.q1('SELECT * FROM products WHERE id = ?', id);
  };
  const insertPending = (userId, product, txId) => {
    db.run(`INSERT INTO purchases (id, user_id, product_id, platform, external_transaction_id, status, amount_minor, currency, purchased_at, created_at)
            VALUES (?,?,?,?,?,?,?,?,?,?)`,
      db.uuid(), userId, product.id, 'google', txId, 'pending', product.price_minor, product.currency, db.now(), db.now());
  };

  test('the pending row is re-keyed from the client token onto the order id', () => {
    const userId = mkUser();
    const product = mkProduct();
    const token = 'tok_' + db.uuid().replaceAll('-', '');
    const orderId = 'GPA.3312-1234-5678-90123';
    insertPending(userId, product, token);

    const canonical = api.adoptCanonicalTxId('google', token, orderId);
    assert.equal(canonical, orderId);
    assert.equal(db.q1('SELECT id FROM purchases WHERE platform=? AND external_transaction_id=?', 'google', token), undefined);
    assert.ok(db.q1('SELECT id FROM purchases WHERE platform=? AND external_transaction_id=?', 'google', orderId));
  });

  test('a replay mapping to the same order id does not leave two rows behind', () => {
    const userId = mkUser();
    const product = mkProduct();
    const orderId = 'GPA.' + db.uuid().slice(0, 18);
    // First attempt, already reconciled onto the order id.
    insertPending(userId, product, orderId);
    db.run(`UPDATE purchases SET status='verified' WHERE external_transaction_id=?`, orderId);
    // Second attempt with a different client token Google maps to the same order.
    const token2 = 'tok2_' + db.uuid().replaceAll('-', '');
    insertPending(userId, product, token2);

    const canonical = api.adoptCanonicalTxId('google', token2, orderId);
    assert.equal(canonical, orderId);
    const rows = db.q('SELECT * FROM purchases WHERE platform=? AND external_transaction_id IN (?,?)', 'google', orderId, token2);
    assert.equal(rows.length, 1, 'the token-keyed placeholder must be gone');
    assert.equal(rows[0].external_transaction_id, orderId);
    assert.equal(rows[0].status, 'verified');
  });

  test('no order id from Google → the (already validated) token stays the key', () => {
    const token = 'tok_' + db.uuid().replaceAll('-', '');
    assert.equal(api.adoptCanonicalTxId('google', token, null), token);
    assert.equal(api.adoptCanonicalTxId('google', token, token), token);
  });

  test('a verified row is never silently deleted by reconciliation', () => {
    const userId = mkUser();
    const product = mkProduct();
    const token = 'tok_' + db.uuid().replaceAll('-', '');
    const orderId = 'GPA.' + db.uuid().slice(0, 18);
    insertPending(userId, product, token);
    db.run(`UPDATE purchases SET status='verified' WHERE external_transaction_id=?`, token);
    insertPending(userId, product, orderId);
    db.run(`UPDATE purchases SET status='verified' WHERE external_transaction_id=?`, orderId);

    api.adoptCanonicalTxId('google', token, orderId);
    // The token row was 'verified', not a placeholder — it must survive so the
    // grant it already made stays auditable.
    assert.ok(db.q1('SELECT id FROM purchases WHERE external_transaction_id=?', token));
    assert.ok(db.q1('SELECT id FROM purchases WHERE external_transaction_id=?', orderId));
  });
});
