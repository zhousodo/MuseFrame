// Credit ledger invariants (spec §13.4). The ledger is the only record that a
// generation was paid for, so the properties that matter are: a reserve is
// idempotent, a release gives the units back exactly once, a commit consumes
// them permanently, and release-after-commit is a no-op (otherwise a succeeded
// job could be refunded and the image delivered for free).
import { test, describe, before } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

process.env.MUSEFRAME_DATA_DIR = mkdtempSync(path.join(tmpdir(), 'mf-ledger-'));

const db = await import('../db.js');
const { grantUnits, reserveUnits, commitUnits, releaseUnits, availableUnits } = await import('../ledger.js');

function mkUser() {
  const id = db.uuid();
  db.run('INSERT INTO users (id, display_name, is_guest, locale, status, created_at, updated_at) VALUES (?,?,?,?,?,?,?)',
    id, 'u', 1, 'en', 'active', db.now(), db.now());
  return id;
}
const entries = (userId) => db.q('SELECT entry_type, units, reference_key FROM credit_ledger WHERE user_id = ? ORDER BY created_at, reference_key', userId);
const countOf = (userId, type) => entries(userId).filter((e) => e.entry_type === type).length;

describe('grant → reserve → commit', () => {
  test('a commit permanently consumes the reserved unit', () => {
    const u = mkUser();
    grantUnits(u, 3, 'free_grant', 'dedupe-' + u);
    assert.equal(availableUnits(u), 3);

    const job = db.uuid();
    reserveUnits(u, job, 1);
    assert.equal(availableUnits(u), 2, 'a reserve is debited immediately');

    commitUnits(u, job);
    assert.equal(availableUnits(u), 2, 'a commit does not move the balance again');
    assert.equal(countOf(u, 'commit'), 1);
  });

  test('commit is idempotent — replaying it never double-charges', () => {
    const u = mkUser();
    grantUnits(u, 2, 'free_grant', 'dedupe-' + u);
    const job = db.uuid();
    reserveUnits(u, job, 1);
    commitUnits(u, job);
    commitUnits(u, job);
    commitUnits(u, job);
    assert.equal(countOf(u, 'commit'), 1);
    assert.equal(availableUnits(u), 1);
  });

  test('a commit entry carries 0 units (the DB CHECK enforces it)', () => {
    const u = mkUser();
    grantUnits(u, 1, 'free_grant', 'dedupe-' + u);
    const job = db.uuid();
    reserveUnits(u, job, 1);
    commitUnits(u, job);
    const c = entries(u).find((e) => e.entry_type === 'commit');
    assert.equal(c.units, 0);
  });
});

describe('reserve', () => {
  test('re-reserving the same job is a no-op, not a second debit', () => {
    const u = mkUser();
    grantUnits(u, 5, 'free_grant', 'dedupe-' + u);
    const job = db.uuid();
    reserveUnits(u, job, 1);
    reserveUnits(u, job, 1);
    reserveUnits(u, job, 1);
    assert.equal(availableUnits(u), 4);
    assert.equal(countOf(u, 'reserve'), 1);
  });

  test('an insufficient balance throws INSUFFICIENT_ENTITLEMENT and writes nothing', () => {
    const u = mkUser();
    grantUnits(u, 1, 'free_grant', 'dedupe-' + u);
    const job1 = db.uuid();
    reserveUnits(u, job1, 1);
    assert.equal(availableUnits(u), 0);

    const job2 = db.uuid();
    assert.throws(() => reserveUnits(u, job2, 1), (e) => {
      assert.equal(e.code, 'INSUFFICIENT_ENTITLEMENT');
      assert.equal(e.details.requiredUnits, 1);
      assert.equal(e.details.availableUnits, 0);
      return true;
    });
    // The failed reserve must have rolled back completely.
    assert.equal(db.q('SELECT id FROM credit_ledger WHERE job_id = ?', job2).length, 0);
    assert.equal(availableUnits(u), 0);
  });

  test('two jobs racing the last unit: exactly one wins (spec §24)', () => {
    const u = mkUser();
    grantUnits(u, 1, 'free_grant', 'dedupe-' + u);
    const a = db.uuid(), b = db.uuid();
    let wins = 0, losses = 0;
    for (const job of [a, b]) {
      try { reserveUnits(u, job, 1); wins++; } catch (e) { if (e.code === 'INSUFFICIENT_ENTITLEMENT') losses++; else throw e; }
    }
    assert.equal(wins, 1);
    assert.equal(losses, 1);
    assert.equal(availableUnits(u), 0);
  });

  test('reserves drain the earliest-expiring bucket first', () => {
    const u = mkUser();
    const soon = new Date(Date.now() + 3600_000).toISOString();
    const later = new Date(Date.now() + 30 * 86400_000).toISOString();
    const lateBucket = grantUnits(u, 1, 'purchase', db.uuid(), later);
    const soonBucket = grantUnits(u, 1, 'purchase', db.uuid(), soon);
    const job = db.uuid();
    reserveUnits(u, job, 1);
    const r = db.q1('SELECT balance_bucket_id FROM credit_ledger WHERE job_id = ? AND entry_type = ?', job, 'reserve');
    assert.equal(r.balance_bucket_id, soonBucket, 'the bucket that expires first must be spent first');
    assert.notEqual(r.balance_bucket_id, lateBucket);
  });

  test('an expired bucket is not spendable', () => {
    const u = mkUser();
    grantUnits(u, 5, 'purchase', db.uuid(), new Date(Date.now() - 1000).toISOString());
    assert.equal(availableUnits(u), 0);
    assert.throws(() => reserveUnits(u, db.uuid(), 1), (e) => e.code === 'INSUFFICIENT_ENTITLEMENT');
  });

  test('a multi-unit reserve can span buckets and still balances', () => {
    const u = mkUser();
    grantUnits(u, 1, 'purchase', db.uuid(), new Date(Date.now() + 3600_000).toISOString());
    grantUnits(u, 2, 'purchase', db.uuid(), new Date(Date.now() + 7200_000).toISOString());
    const job = db.uuid();
    reserveUnits(u, job, 3);
    assert.equal(availableUnits(u), 0);
    const rs = db.q('SELECT units FROM credit_ledger WHERE job_id = ? AND entry_type = ?', job, 'reserve');
    assert.equal(rs.length, 2, 'one entry per bucket touched');
    assert.equal(rs.reduce((s, r) => s + r.units, 0), -3);
  });
});

describe('release', () => {
  test('a failed job returns the unit — 0 charged (spec §24)', () => {
    const u = mkUser();
    grantUnits(u, 1, 'free_grant', 'dedupe-' + u);
    const job = db.uuid();
    reserveUnits(u, job, 1);
    assert.equal(availableUnits(u), 0);
    releaseUnits(u, job);
    assert.equal(availableUnits(u), 1, 'the unit is back');
  });

  test('release is idempotent — it cannot mint units by being replayed', () => {
    const u = mkUser();
    grantUnits(u, 1, 'free_grant', 'dedupe-' + u);
    const job = db.uuid();
    reserveUnits(u, job, 1);
    releaseUnits(u, job);
    releaseUnits(u, job);
    releaseUnits(u, job);
    assert.equal(availableUnits(u), 1);
    assert.equal(countOf(u, 'release'), 1);
  });

  test('release AFTER commit is a no-op — a delivered image is never refunded', () => {
    const u = mkUser();
    grantUnits(u, 1, 'free_grant', 'dedupe-' + u);
    const job = db.uuid();
    reserveUnits(u, job, 1);
    commitUnits(u, job);
    releaseUnits(u, job);
    assert.equal(availableUnits(u), 0, 'the unit stays spent');
    assert.equal(countOf(u, 'release'), 0);
  });

  test('releasing a job that never reserved does nothing', () => {
    const u = mkUser();
    grantUnits(u, 1, 'free_grant', 'dedupe-' + u);
    releaseUnits(u, db.uuid());
    assert.equal(availableUnits(u), 1);
    assert.equal(countOf(u, 'release'), 0);
  });

  test('reserve → release → reserve again reuses the same unit exactly once', () => {
    const u = mkUser();
    grantUnits(u, 1, 'free_grant', 'dedupe-' + u);
    const first = db.uuid(), second = db.uuid();
    reserveUnits(u, first, 1);
    releaseUnits(u, first);
    reserveUnits(u, second, 1);
    assert.equal(availableUnits(u), 0);
    assert.throws(() => reserveUnits(u, db.uuid(), 1), (e) => e.code === 'INSUFFICIENT_ENTITLEMENT');
  });

  test('the balance is exactly the sum of the ledger (no hidden state)', () => {
    const u = mkUser();
    grantUnits(u, 4, 'free_grant', 'dedupe-' + u);
    const a = db.uuid(), b = db.uuid(), c = db.uuid();
    reserveUnits(u, a, 1); commitUnits(u, a);
    reserveUnits(u, b, 1); releaseUnits(u, b);
    reserveUnits(u, c, 2);
    const sum = entries(u).reduce((s, e) => s + e.units, 0);
    assert.equal(availableUnits(u), sum);
    assert.equal(availableUnits(u), 1); // 4 - 1 committed - 2 held
  });
});

describe('grant idempotency', () => {
  test('the same free-grant dedupe key cannot grant twice', () => {
    const u = mkUser();
    const key = 'device-' + u;
    grantUnits(u, 1, 'free_grant', key);
    assert.throws(() => grantUnits(u, 1, 'free_grant', key), /UNIQUE|constraint/i);
    assert.equal(availableUnits(u), 1);
  });

  test('a subscription renewal grants once per period via referenceId (F11)', () => {
    const u = mkUser();
    const purchaseId = db.uuid();
    const p1 = '2026-10-01T00:00:00.000Z';
    const p2 = '2026-11-01T00:00:00.000Z';
    grantUnits(u, 10, 'purchase', purchaseId, p1, `${purchaseId}:${p1}`);
    // Replaying the same period must not grant again...
    assert.throws(() => grantUnits(u, 10, 'purchase', purchaseId, p1, `${purchaseId}:${p1}`), /UNIQUE|constraint/i);
    assert.equal(availableUnits(u), 10);
    // ...but the next billing period must.
    grantUnits(u, 10, 'purchase', purchaseId, p2, `${purchaseId}:${p2}`);
    assert.equal(availableUnits(u), 20);
  });
});

describe('tx() re-entrancy (F38)', () => {
  test('a nested tx joins the outer one and rolls back with it', async () => {
    const u = mkUser();
    grantUnits(u, 1, 'free_grant', 'dedupe-' + u);
    const job = db.uuid();
    assert.throws(() => {
      db.tx(() => {
        reserveUnits(u, job, 1);   // opens its own tx() internally
        throw new Error('boom after the reserve');
      });
    }, /boom/);
    // The whole unit of work must be gone: this is the property that lets the
    // job INSERT and its reserve commit together or not at all.
    assert.equal(db.q('SELECT id FROM credit_ledger WHERE job_id = ?', job).length, 0);
    assert.equal(availableUnits(u), 1);
  });

  test('a nested tx that succeeds commits exactly once', () => {
    const u = mkUser();
    grantUnits(u, 1, 'free_grant', 'dedupe-' + u);
    const job = db.uuid();
    db.tx(() => {
      reserveUnits(u, job, 1);
      commitUnits(u, job);
    });
    assert.equal(availableUnits(u), 0);
    assert.equal(countOf(u, 'reserve'), 1);
    assert.equal(countOf(u, 'commit'), 1);
  });
});
