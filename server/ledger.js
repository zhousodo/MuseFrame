// Append-only credit ledger (spec §13.4). Balances are projections over entries;
// reserve → commit (success) or reserve → release (failure/cancel). Every path is
// idempotent via unique (user_id, reference_key).
import { db, q, q1, run, tx, uuid, now } from './db.js';

export function grantUnits(userId, units, sourceType, sourceId, expiresAt = null) {
  return tx(() => {
    const bucketId = uuid();
    run('INSERT INTO credit_buckets (id, user_id, source_type, source_id, granted_units, expires_at, created_at) VALUES (?,?,?,?,?,?,?)',
      bucketId, userId, sourceType, sourceId, units, expiresAt, now());
    run(`INSERT INTO credit_ledger (id, user_id, entry_type, units, balance_bucket_id, purchase_id, reference_key, created_at)
         VALUES (?,?,?,?,?,?,?,?)`,
      uuid(), userId, 'grant', units, bucketId, sourceType === 'purchase' ? sourceId : null,
      `grant:${sourceType}:${sourceId ?? bucketId}`, now());
    return bucketId;
  });
}

function bucketBalances(userId) {
  return q(`
    SELECT b.id, b.expires_at, b.created_at, COALESCE(SUM(l.units), 0) AS balance
    FROM credit_buckets b
    LEFT JOIN credit_ledger l ON l.balance_bucket_id = b.id
    WHERE b.user_id = ? AND (b.expires_at IS NULL OR b.expires_at > ?)
    GROUP BY b.id
    HAVING balance > 0
    ORDER BY b.expires_at IS NULL, b.expires_at, b.created_at`, userId, now());
}

export function availableUnits(userId) {
  return bucketBalances(userId).reduce((s, b) => s + b.balance, 0);
}

/**
 * Reserve `units` for a job. Consumes earliest-expiring buckets first.
 * Throws {code:'INSUFFICIENT_ENTITLEMENT'} when balance is short.
 * Idempotent per job: re-reserving an already reserved job is a no-op.
 */
export function reserveUnits(userId, jobId, units) {
  return tx(() => {
    const existing = q1('SELECT id FROM credit_ledger WHERE user_id = ? AND reference_key LIKE ?', userId, `job:${jobId}:reserve%`);
    if (existing) return;
    const buckets = bucketBalances(userId);
    const total = buckets.reduce((s, b) => s + b.balance, 0);
    if (total < units) {
      const err = new Error('A standard image is required.');
      err.code = 'INSUFFICIENT_ENTITLEMENT';
      err.details = { requiredUnits: units, availableUnits: total };
      throw err;
    }
    let remaining = units, part = 0;
    for (const b of buckets) {
      if (remaining <= 0) break;
      const take = Math.min(b.balance, remaining);
      run(`INSERT INTO credit_ledger (id, user_id, entry_type, units, balance_bucket_id, job_id, reference_key, created_at)
           VALUES (?,?,?,?,?,?,?,?)`,
        uuid(), userId, 'reserve', -take, b.id, jobId, `job:${jobId}:reserve:${part++}`, now());
      remaining -= take;
    }
  });
}

/** Finalize a successful job: reserve becomes consumed (commit entries carry 0 units). */
export function commitUnits(userId, jobId) {
  return tx(() => {
    if (q1('SELECT id FROM credit_ledger WHERE user_id = ? AND reference_key = ?', userId, `job:${jobId}:commit`)) return;
    const reserves = q('SELECT balance_bucket_id FROM credit_ledger WHERE user_id = ? AND job_id = ? AND entry_type = ?', userId, jobId, 'reserve');
    if (!reserves.length) return;
    run(`INSERT INTO credit_ledger (id, user_id, entry_type, units, balance_bucket_id, job_id, reference_key, created_at)
         VALUES (?,?,?,?,?,?,?,?)`,
      uuid(), userId, 'commit', 0, reserves[0].balance_bucket_id, jobId, `job:${jobId}:commit`, now());
  });
}

/** Failed/cancelled job: return every reserved unit to its bucket. */
export function releaseUnits(userId, jobId) {
  return tx(() => {
    if (q1('SELECT id FROM credit_ledger WHERE user_id = ? AND reference_key LIKE ?', userId, `job:${jobId}:release%`)) return;
    if (q1('SELECT id FROM credit_ledger WHERE user_id = ? AND reference_key = ?', userId, `job:${jobId}:commit`)) return; // already committed
    const reserves = q('SELECT balance_bucket_id, units FROM credit_ledger WHERE user_id = ? AND job_id = ? AND entry_type = ?', userId, jobId, 'reserve');
    reserves.forEach((r, i) => {
      run(`INSERT INTO credit_ledger (id, user_id, entry_type, units, balance_bucket_id, job_id, reference_key, created_at)
           VALUES (?,?,?,?,?,?,?,?)`,
        uuid(), userId, 'release', -r.units, r.balance_bucket_id, jobId, `job:${jobId}:release:${i}`, now());
    });
  });
}
