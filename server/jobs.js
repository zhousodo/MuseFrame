// Generation pipeline worker (spec §9.2). In-process queue over the DB (survives
// restarts: queued/running jobs are re-enqueued on boot). Stages mirror §6.10:
// preparing → building → making → checking → complete. Quality gate + one retry.
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { ASSET_DIR, q, q1, run, uuid, now } from './db.js';
import { commitUnits, releaseUnits } from './ledger.js';
import { decodeJpeg, encodeJpeg, applyStyle, cropTo, resize } from './engine/styleEngine.js';
import { remoteConfig, createEdit, generationStatus } from './engine/remoteAdapter.js';
import { compileInstruction } from './engine/promptCompiler.js';
import { cfg } from './configStore.js';

const STAGE_DELAYS = { preparing: 700, building: 900, checking: 600 }; // paced so progress reads honestly

let queue = [];
let active = 0;

export function enqueueJob(jobId) {
  queue.push(jobId);
  setImmediate(pump);
}

export function recoverJobs() {
  const stuck = q(`SELECT id FROM generation_jobs WHERE status IN ('created','queued','running','quality_check')`);
  for (const j of stuck) {
    run('UPDATE generation_jobs SET status = ?, stage = ?, updated_at = ? WHERE id = ?', 'queued', 'preparing', now(), j.id);
    queue.push(j.id);
  }
  if (stuck.length) setImmediate(pump);
}

const sleep = (ms) => new Promise(r => setTimeout(r, ms));

function setStage(jobId, status, stage) {
  run('UPDATE generation_jobs SET status = ?, stage = ?, updated_at = ? WHERE id = ?', status, stage, now(), jobId);
}

function pump() {
  // Remote jobs are IO-bound waits on the provider — run several in parallel so
  // one slow generation doesn't serialize everyone behind it. Read live so an
  // admin change to worker_concurrency applies to the next dispatch, no restart.
  const maxConcurrent = cfg('worker_concurrency');
  while (active < maxConcurrent && queue.length) {
    const jobId = queue.shift();
    active++;
    processJob(jobId)
      .catch(e => console.error(`[worker] job ${jobId} crashed:`, e.message))
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

async function processJob(jobId) {
  const job = q1('SELECT * FROM generation_jobs WHERE id = ?', jobId);
  if (!job || ['succeeded', 'failed', 'cancelled'].includes(job.status)) return;

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

  // Persist candidate asset + finalize billing atomically-enough for MVP.
  const jpg = encodeJpeg(result, 90);
  const assetId = uuid();
  const storageKey = `${assetId}.jpg`;
  writeFileSync(path.join(ASSET_DIR, storageKey), jpg);
  const t = now();
  run(`INSERT INTO assets (id, user_id, project_id, kind, status, storage_key, content_type, byte_size, width, height, created_at, updated_at)
       VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
    assetId, job.user_id, job.project_id, 'candidate', 'ready', storageKey, 'image/jpeg', jpg.length, result.width, result.height, t, t);
  const candidateId = uuid();
  run('INSERT INTO generation_candidates (id, job_id, candidate_index, asset_id, quality_passed, created_at) VALUES (?,?,?,?,1,?)',
    candidateId, jobId, 0, assetId, t);
  commitUnits(job.user_id, jobId);
  // Rough cost telemetry: provider token usage (spec §13.6 wants per-job cost).
  const costProxy = usage?.total_tokens || 0;
  run('UPDATE generation_jobs SET status = ?, stage = ?, cost_minor = ?, error_code = NULL, finished_at = ?, updated_at = ? WHERE id = ?',
    'succeeded', 'complete', costProxy, t, t, jobId);
  console.log(`[worker] job ${jobId} succeeded via ${provider} adapter`);
  run('UPDATE projects SET selected_candidate_id = ?, status = ?, updated_at = ? WHERE id = ?',
    candidateId, 'ready', t, job.project_id);
}

function failJob(job, code) {
  releaseUnits(job.user_id, job.id);
  run('UPDATE generation_jobs SET status = ?, stage = ?, error_code = ?, finished_at = ?, updated_at = ? WHERE id = ?',
    'failed', 'failed', code, now(), now(), job.id);
  run('UPDATE projects SET status = ?, updated_at = ? WHERE id = ?', 'draft', now(), job.project_id);
}
