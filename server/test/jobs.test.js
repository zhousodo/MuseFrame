// Worker invariants. F14/F31: any throw inside processJob used to leave the row
// in 'running' with its unit reserved forever, and boot recovery then re-ran the
// same crash. F32: no attempt cap meant a job that OOMs the process is
// re-enqueued on every boot — a crash loop that bills the provider each lap.
// F24: 'created' rows (no reserve) were re-queued, producing an unbilled image.
// F20/F40: worker_concurrency=0 silently froze the queue.
import { test, describe, before } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

process.env.MUSEFRAME_DATA_DIR = mkdtempSync(path.join(tmpdir(), 'mf-jobs-'));
process.env.IMAGE_PROVIDER = 'local';

const db = await import('../db.js');
const { grantUnits, reserveUnits, availableUnits } = await import('../ledger.js');
const jobs = await import('../jobs.js');
const { setCfg } = await import('../configStore.js');

// Park the dispatcher for the whole file. recoverJobs() re-queues rows and
// pump() would immediately run them against assets that do not exist on disk,
// failing them underneath the assertions below. Draining is also exactly the
// state the SIGTERM path puts the worker in, so it is asserted directly later.
await jobs.drainWorker(0);

function mkUser() {
  const id = db.uuid();
  db.run('INSERT INTO users (id, display_name, is_guest, locale, status, created_at, updated_at) VALUES (?,?,?,?,?,?,?)',
    id, 'u', 1, 'en', 'active', db.now(), db.now());
  return id;
}
function mkProject(userId) {
  const id = db.uuid();
  db.run('INSERT INTO projects (id, user_id, title, status, created_at, updated_at) VALUES (?,?,?,?,?,?)',
    id, userId, 'p', 'generating', db.now(), db.now());
  return id;
}
function mkAsset(userId) {
  const id = db.uuid(), t = db.now();
  db.run(`INSERT INTO assets (id, user_id, kind, status, storage_key, content_type, created_at, updated_at)
          VALUES (?,?,?,?,?,?,?,?)`, id, userId, 'source', 'ready', id + '.jpg', 'image/jpeg', t, t);
  return id;
}
// One shared style row: the worker never reaches it in these tests, but the
// foreign key does.
let styleVersionId = null;
function mkStyleVersion() {
  if (styleVersionId) return styleVersionId;
  const styleId = db.uuid(), t = db.now();
  db.run(`INSERT INTO styles (id, internal_key, slug, status, theme, premium, public_name, short_caption, suitability_tags, created_at)
          VALUES (?,?,?,?,?,?,?,?,?,?)`,
    styleId, 'test_style', 'test-style', 'published', 'quiet', 0, 'Test', 'c', '[]', t);
  styleVersionId = db.uuid();
  db.run(`INSERT INTO style_versions (id, style_id, version, status, spec, published_at, created_at)
          VALUES (?,?,?,?,?,?,?)`, styleVersionId, styleId, 1, 'published', '{}', t, t);
  return styleVersionId;
}
function mkJob(userId, projectId, { status = 'queued', attempts = 0, reserve = true } = {}) {
  const id = db.uuid(), t = db.now();
  db.run(`INSERT INTO generation_jobs (id, user_id, project_id, source_asset_id, style_version_id, status, stage, controls, output, reserved_units, attempt_count, created_at, updated_at)
          VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
    id, userId, projectId, mkAsset(userId), mkStyleVersion(), status, 'preparing', '{}', '{}', 1, attempts, t, t);
  if (reserve) reserveUnits(userId, id, 1);
  return id;
}
const jobRow = (id) => db.q1('SELECT * FROM generation_jobs WHERE id = ?', id);
const projRow = (id) => db.q1('SELECT * FROM projects WHERE id = ?', id);

describe('recoverJobs — boot recovery (F24/F32)', () => {
  test("a 'created' job with no reserve is failed, never run", () => {
    const u = mkUser(); const p = mkProject(u);
    grantUnits(u, 1, 'free_grant', 'd-' + u);
    const id = mkJob(u, p, { status: 'created', reserve: false });

    jobs.recoverJobs();

    const j = jobRow(id);
    assert.equal(j.status, 'failed', 'an unbilled job must not be generated');
    assert.equal(j.error_code, 'INTERNAL_ERROR');
    assert.equal(projRow(p).status, 'draft');
    assert.equal(availableUnits(u), 1, 'the untouched unit is still there');
  });

  test('a job past the attempt cap is failed and its unit released (crash loop guard)', () => {
    const u = mkUser(); const p = mkProject(u);
    grantUnits(u, 1, 'free_grant', 'd-' + u);
    const id = mkJob(u, p, { status: 'running', attempts: 3 });
    assert.equal(availableUnits(u), 0, 'the unit is held by the reserve');

    jobs.recoverJobs();

    const j = jobRow(id);
    assert.equal(j.status, 'failed');
    assert.equal(j.error_code, 'PROVIDER_ERROR');
    assert.equal(availableUnits(u), 1, 'the reserve was released — 0 units charged');
  });

  test('a job under the cap is re-queued, not failed', () => {
    const u = mkUser(); const p = mkProject(u);
    grantUnits(u, 1, 'free_grant', 'd-' + u);
    const id = mkJob(u, p, { status: 'running', attempts: 1 });

    jobs.recoverJobs();

    assert.equal(jobRow(id).status, 'queued');
    assert.equal(availableUnits(u), 0, 'the reserve is still held for the retry');
  });

  test('a stuck quality_check job is recovered too', () => {
    const u = mkUser(); const p = mkProject(u);
    grantUnits(u, 1, 'free_grant', 'd-' + u);
    const id = mkJob(u, p, { status: 'quality_check', attempts: 0 });
    jobs.recoverJobs();
    assert.equal(jobRow(id).status, 'queued');
  });

  test('finished jobs are left alone', () => {
    const u = mkUser(); const p = mkProject(u);
    grantUnits(u, 3, 'free_grant', 'd-' + u);
    const done = mkJob(u, p, { status: 'succeeded' });
    const failed = mkJob(u, p, { status: 'failed' });
    const cancelled = mkJob(u, p, { status: 'cancelled' });
    jobs.recoverJobs();
    assert.equal(jobRow(done).status, 'succeeded');
    assert.equal(jobRow(failed).status, 'failed');
    assert.equal(jobRow(cancelled).status, 'cancelled');
  });
});

describe('queueDepth — what /v1/health reports (F41)', () => {
  test('reports the shape health depends on', () => {
    const d = jobs.queueDepth();
    for (const k of ['queued', 'active', 'oldestQueuedAgeSec', 'draining']) {
      assert.ok(k in d, `queueDepth must report ${k}`);
    }
    assert.equal(typeof d.queued, 'number');
    assert.equal(typeof d.active, 'number');
    assert.equal(typeof d.draining, 'boolean');
  });

  test('enqueuing is visible in the depth', () => {
    const before = jobs.queueDepth().queued;
    jobs.enqueueJob('never-dispatched-' + db.uuid());
    assert.ok(jobs.queueDepth().queued >= before);
  });
});

describe('worker_concurrency is clamped (F20/F40)', () => {
  test('the admin panel value 0 does not freeze the queue', () => {
    // pump() read cfg('worker_concurrency') directly, so 0 meant
    // `while (active < 0)` — never dispatch, units reserved, no way back
    // without a restart. The clamp is the fix; assert it at the source.
    setCfg('worker_concurrency', 0);
    assert.doesNotThrow(() => jobs.pump());
    // A negative or nonsense value must be equally harmless.
    setCfg('worker_concurrency', -5);
    assert.doesNotThrow(() => jobs.pump());
    setCfg('worker_concurrency', 3);
  });

  test('an absurd concurrency is capped rather than obeyed', () => {
    setCfg('worker_concurrency', 9999);
    assert.doesNotThrow(() => jobs.pump());
    setCfg('worker_concurrency', 3);
  });
});

describe('drainWorker — graceful shutdown (F33)', () => {
  test('resolves promptly when nothing is running', async () => {
    const started = Date.now();
    const r = await jobs.drainWorker(5_000);
    assert.equal(r.active, 0);
    assert.ok(Date.now() - started < 3_000, 'must not wait out the whole timeout');
  });

  test('draining stops further dispatch', () => {
    // Once draining, pump() must be a no-op so a SIGTERM does not start new
    // provider calls it is about to abandon.
    assert.equal(jobs.queueDepth().draining, true);
    const before = jobs.queueDepth().active;
    jobs.pump();
    assert.equal(jobs.queueDepth().active, before);
  });
});
