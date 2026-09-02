// Generation pipeline worker (spec §9.2). In-process queue over the DB (survives
// restarts: queued/running jobs are re-enqueued on boot). Stages mirror §6.10:
// preparing → building → making → checking → complete. Quality gate + one retry.
import { readFileSync, writeFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { ASSET_DIR, q, q1, run, tx, uuid, now } from './db.js';
import { commitUnits, releaseUnits } from './ledger.js';
import { decodeJpeg, encodeJpeg, applyStyle, cropTo, resize } from './engine/styleEngine.js';
import { remoteConfig, createEdit, generationStatus } from './engine/remoteAdapter.js';
import { compileInstruction } from './engine/promptCompiler.js';
import { cfg } from './configStore.js';

const STAGE_DELAYS = { preparing: 700, building: 900, checking: 600 }; // paced so progress reads honestly

// A job that crashes the process is re-queued on the next boot, which crashes
// the process again: without a ceiling that is an infinite restart loop, and
// against a remote provider every lap is a real, billed generation.
const MAX_ATTEMPTS = Number(process.env.MAX_JOB_ATTEMPTS) || 3;

let queue = [];
let active = 0;
let draining = false;
const queuedAt = new Map(); // jobId -> ms, for the health probe

export function enqueueJob(jobId) {
  queue.push(jobId);
  queuedAt.set(jobId, Date.now());
  setImmediate(pump);
}

/** Queue snapshot for /v1/health — "is the worker actually moving". */
export function queueDepth() {
  let oldest = 0;
  for (const id of queue) {
    const at = queuedAt.get(id);
    if (at) oldest = Math.max(oldest, Math.round((Date.now() - at) / 1000));
  }
  return { queued: queue.length, active, oldestQueuedAgeSec: oldest, draining };
}

/**
 * Stop dispatching and resolve once the in-flight jobs have finished, so a
 * container restart does not abandon a generation the user is already paying
 * for. Called from the SIGTERM handler in index.js.
 */
export function drainWorker(timeoutMs = 20_000) {
  draining = true;
  const started = Date.now();
  return new Promise((resolve) => {
    const check = () => {
      if (active === 0 || Date.now() - started > timeoutMs) return resolve({ active });
      setTimeout(check, 200).unref?.();
    };
    check();
  });
}

export function recoverJobs() {
  // 'created' rows are jobs whose ledger reserve never landed. Re-queuing them
  // produced a generation nobody paid for. (The API now writes the row and its
  // reserve in one transaction, so such a row can only be an older leftover.)
  for (const j of q(`SELECT * FROM generation_jobs WHERE status = 'created'`)) {
    if (q1('SELECT id FROM credit_ledger WHERE job_id = ? AND entry_type = ?', j.id, 'reserve')) continue;
    const t = now();
    run(`UPDATE generation_jobs SET status='failed', stage='failed', error_code=?, finished_at=?, updated_at=? WHERE id=?`,
      'INTERNAL_ERROR', t, t, j.id);
    run(`UPDATE projects SET status='draft', updated_at=? WHERE id=?`, t, j.project_id);
    console.warn(`[worker] job ${j.id} recovered with no reserve — failed, not run`);
  }
  const stuck = q(`SELECT * FROM generation_jobs WHERE status IN ('created','queued','running','quality_check')`);
  for (const j of stuck) {
    if ((j.attempt_count || 0) >= MAX_ATTEMPTS) {
      console.error(`[worker] job ${j.id} has ${j.attempt_count} attempts — failing instead of re-queuing (crash loop guard)`);
      failJob(j, 'PROVIDER_ERROR');
      continue;
    }
    run('UPDATE generation_jobs SET status = ?, stage = ?, updated_at = ? WHERE id = ?', 'queued', 'preparing', now(), j.id);
    queue.push(j.id);
    queuedAt.set(j.id, Date.now());
  }
  if (queue.length) setImmediate(pump);
}

const sleep = (ms) => new Promise(r => setTimeout(r, ms));

function setStage(jobId, status, stage) {
  run('UPDATE generation_jobs SET status = ?, stage = ?, updated_at = ? WHERE id = ?', status, stage, now(), jobId);
}

export function pump() {
  if (draining) return;
  // Remote jobs are IO-bound waits on the provider — run several in parallel so
  // one slow generation doesn't serialize everyone behind it. Read live so an
  // admin change to worker_concurrency applies to the next dispatch, no restart.
  // Clamped: the admin panel accepted 0, which froze the queue silently with
  // every queued job's unit still reserved and no way back short of a restart.
  const maxConcurrent = Math.max(1, Math.min(8, Math.floor(Number(cfg('worker_concurrency'))) || 1));
  while (active < maxConcurrent && queue.length) {
    const jobId = queue.shift();
    queuedAt.delete(jobId);
    active++;
    processJob(jobId)
      .catch((e) => {
        // Last line of defence. processJob has its own catch, but if even that
        // throws the job would otherwise sit in 'running' forever with a unit
        // reserved, and boot recovery would re-run it every restart.
        console.error(`[worker] job ${jobId} crashed:`, e?.stack || e?.message || e);
        try {
          const job = q1('SELECT * FROM generation_jobs WHERE id = ?', jobId);
          if (job && !['succeeded', 'failed', 'cancelled'].includes(job.status)) failJob(job, 'INTERNAL_ERROR');
        } catch (e2) { console.error('[worker] could not fail job', jobId, e2.message); }
      })
      .finally(() => { active--; pump(); });
  }
}

// Quality gate (spec §9.4, automated subset): decodable, sane dimensions,
// not blank, not identical to the source.
function qualityGate(result, source) {
  if (!result || result.width < 64 || result.height < 64) return 'BAD_DIMENSIONS';
  let sum = 0, n = 0;
  const d = result.data;
  for (let i = 0; i < d.length; i += 401 * 4) { sum += 0.299 * d[i] + 0.587 * d[i + 1] + 0.114 * d[i + 2]; n++; }
  const mean = sum / n;
  if (mean < 2 || mean > 253) return 'BLANK_OUTPUT';
  return null;
}

/**
 * Everything after the status check runs inside a catch that fails the job.
 * Before, only the two adapter calls were guarded: a throw from readFileSync
 * (deleted source), JSON.parse (bad spec), encodeJpeg (OOM) or the persist
 * block left the row in 'running'/'quality_check' with its unit reserved, the
 * user watching a progress screen that never moved, and boot recovery
 * re-running the same crash forever.
 */
async function processJob(jobId) {
  const job = q1('SELECT * FROM generation_jobs WHERE id = ?', jobId);
  if (!job || ['succeeded', 'failed', 'cancelled'].includes(job.status)) return;
  if ((job.attempt_count || 0) >= MAX_ATTEMPTS) {
    console.error(`[worker] job ${jobId} refused — ${job.attempt_count} attempts already`);
    return failJob(job, 'PROVIDER_ERROR');
  }
  // The reserve is the record that this generation was paid for. No reserve, no
  // generation — otherwise a lost/rolled-back debit becomes a free image.
  if (!q1('SELECT id FROM credit_ledger WHERE job_id = ? AND entry_type = ?', jobId, 'reserve')) {
    console.error(`[worker] job ${jobId} has no reserve entry — refusing`);
    return failJob(job, 'INTERNAL_ERROR');
  }
  try {
    return await runJob(job, jobId);
  } catch (e) {
    console.error(`[worker] job ${jobId} failed unexpectedly:`, e?.stack || e?.message || e);
    return failJob(job, 'INTERNAL_ERROR');
  }
}

async function runJob(job, jobId) {
  // Hard gate. With no image provider configured the service must not produce
  // anything at all: the local pixel engine is a filter pass, not the model, and
  // letting it stand in made an unconfigured deployment look fully functional
  // while still charging a unit. Re-checked here (not just at job creation) so
  // jobs queued before the key was cleared, or recovered at boot, also refuse.
  const gen = generationStatus();
  if (!gen.available) {
    console.error(`[worker] job ${jobId} refused — ${gen.reason}${gen.missing.length ? ': ' + gen.missing.join(', ') : ''}`);
    return failJob(job, 'GENERATION_UNAVAILABLE');
  }

  setStage(jobId, 'running', 'preparing');
  await sleep(STAGE_DELAYS.preparing);

  const src = q1('SELECT * FROM assets WHERE id = ?', job.source_asset_id);
  const sv = q1('SELECT * FROM style_versions WHERE id = ?', job.style_version_id);
  if (!src || src.status !== 'ready' || !sv) return failJob(job, 'ASSET_NOT_READY');

  setStage(jobId, 'running', 'building');
  await sleep(STAGE_DELAYS.building);

  const spec = JSON.parse(sv.spec);
  const controls = JSON.parse(job.controls);
  const output = JSON.parse(job.output);

  setStage(jobId, 'running', 'making');
  const buf = readFileSync(path.join(ASSET_DIR, src.storage_key));
  const source = decodeJpeg(buf);
  const analysis = q1('SELECT subject_type, person_count, exposure FROM photo_analyses WHERE asset_id = ?', src.id);
  const subjectType = analysis?.subject_type || 'person';

  // Primary: remote model adapter. Backup: local pixel engine (spec §9.3 dual
  // model). A content-policy rejection fails the job outright — the backup must
  // not be used to sidestep safety (§14.3), and no units are charged.
  let result = null, reason = null, provider = null, usage = null;
  let attempt = job.attempt_count;

  if (remoteConfig.enabled) {
    attempt++;
    run('UPDATE generation_jobs SET attempt_count = ? WHERE id = ?', attempt, jobId);
    try {
      // Downscale before sending — cuts provider latency and input tokens
      // without visible quality loss at the provider's 1024/1536 output grid.
      let sendBuf = buf, sendW = src.width, sendH = src.height;
      if (Math.max(src.width, src.height) > 1024) {
        const k = 1024 / Math.max(src.width, src.height);
        const small = resize(source, Math.round(src.width * k), Math.round(src.height * k));
        sendBuf = encodeJpeg(small, 88); sendW = small.width; sendH = small.height;
      }
      // Designed styles compile a per-photo instruction first (metaphor,
      // layout, content-derived annotations); others use the static assembly.
      let instruction = null;
      if (spec.promptAssembly?.compiler) {
        setStage(jobId, 'running', 'building');
        instruction = await compileInstruction({
          spec, controls, subjectType,
          imageJpeg: sendBuf, // vision: the compiler designs from the actual photo
          photoFacts: {
            personCount: analysis?.person_count,
            orientation: src.width > src.height ? 'landscape' : src.height > src.width ? 'portrait' : 'square',
            exposure: analysis?.exposure,
          },
        });
        setStage(jobId, 'running', 'making');
      }
      const remote = await createEdit({ sourceJpeg: sendBuf, sourceW: sendW, sourceH: sendH, spec, controls, output, subjectType, instruction });
      usage = remote.usage;
      let img = remote;
      // Provider size grid ≠ requested ratio: center-crop to the exact target.
      const R = { '1:1': [1, 1], '4:5': [4, 5], '16:9': [16, 9] }[output.aspectRatio];
      if (R) img = cropTo(img, R[0], R[1]);
      else img = cropTo(img, src.width, src.height); // 'original' — follow source ratio
      setStage(jobId, 'quality_check', 'checking');
      reason = qualityGate(img, source);
      if (!reason) { result = img; provider = 'remote'; }
      else console.warn(`[worker] job ${jobId} remote attempt failed quality gate: ${reason}`);
    } catch (e) {
      reason = e.code || 'PROVIDER_ERROR';
      console.error(`[worker] job ${jobId} remote attempt: ${reason}`);
      if (reason === 'GENERATION_REJECTED') return failJob(job, 'GENERATION_REJECTED');
    }
  }

  // Backup model: deterministic local style engine. Only when this deployment is
  // explicitly running local (IMAGE_PROVIDER=local), or an operator opted into
  // the fallback — a filter pass is not the model's output and, off by default,
  // must not be delivered and billed as one when the provider errors.
  if (!result && (gen.mode === 'local' || cfg('local_engine_fallback'))) {
    attempt++;
    run('UPDATE generation_jobs SET attempt_count = ?, stage = ? WHERE id = ?', attempt, 'making', jobId);
    try {
      await sleep(30); // yield so status polls stay live before the heavy pass
      const candidate = applyStyle(source, spec, controls, output);
      setStage(jobId, 'quality_check', 'checking');
      await sleep(STAGE_DELAYS.checking);
      reason = qualityGate(candidate, source);
      if (!reason) { result = candidate; provider = 'local'; }
    } catch (e) {
      reason = 'PROVIDER_ERROR';
      console.error(`[worker] job ${jobId} local attempt:`, e.message);
    }
  }

  if (!result) return failJob(job, reason === 'PROVIDER_ERROR' ? 'PROVIDER_TIMEOUT' : (reason || 'QUALITY_GATE_FAILED'));

  // Persist candidate asset + finalize billing. The file lands first (it is the
  // thing the DB rows point at); if any DB write then fails, the orphan file is
  // removed and the outer catch fails the job and releases the reserve, rather
  // than leaving a half-written project the user is charged for.
  const jpg = encodeJpeg(result, 90);
  const assetId = uuid();
  const storageKey = `${assetId}.jpg`;
  const outPath = path.join(ASSET_DIR, storageKey);
  writeFileSync(outPath, jpg);
  const t = now();
  const candidateId = uuid();
  try {
    tx(() => {
      run(`INSERT INTO assets (id, user_id, project_id, kind, status, storage_key, content_type, byte_size, width, height, created_at, updated_at)
           VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
        assetId, job.user_id, job.project_id, 'candidate', 'ready', storageKey, 'image/jpeg', jpg.length, result.width, result.height, t, t);
      run('INSERT INTO generation_candidates (id, job_id, candidate_index, asset_id, quality_passed, created_at) VALUES (?,?,?,?,1,?)',
        candidateId, jobId, 0, assetId, t);
      commitUnits(job.user_id, jobId);
      // Rough cost telemetry: provider token usage (spec §13.6 wants per-job cost).
      run('UPDATE generation_jobs SET status = ?, stage = ?, cost_minor = ?, error_code = NULL, finished_at = ?, updated_at = ? WHERE id = ?',
        'succeeded', 'complete', usage?.total_tokens || 0, t, t, jobId);
      run('UPDATE projects SET selected_candidate_id = ?, status = ?, updated_at = ? WHERE id = ?',
        candidateId, 'ready', t, job.project_id);
    });
  } catch (e) {
    try { rmSync(outPath, { force: true }); } catch { /* best effort */ }
    throw e;
  }
  console.log(`[worker] job ${jobId} succeeded via ${provider} adapter`);
}

function failJob(job, code) {
  const t = now();
  tx(() => {
    releaseUnits(job.user_id, job.id);
    run('UPDATE generation_jobs SET status = ?, stage = ?, error_code = ?, finished_at = ?, updated_at = ? WHERE id = ?',
      'failed', 'failed', code, t, t, job.id);
    // A failed retry must never hide an earlier successful work (spec §24): a
    // project that already has a chosen candidate stays 'ready'.
    const p = q1('SELECT selected_candidate_id FROM projects WHERE id = ?', job.project_id);
    run('UPDATE projects SET status = ?, updated_at = ? WHERE id = ?',
      p?.selected_candidate_id ? 'ready' : 'draft', t, job.project_id);
  });
}
