# MuseFrame — Curated image making (MVP)

A working full-stack implementation of the **MuseFrame Gallery MVP Implementation Spec v1.0**:
a curated-gallery photo app where users pick a *direction*, not a prompt — one photo in,
one identity-preserving artwork out, saved in about two minutes.

## Run

```bash
npm install
npm start
# → http://localhost:8787
```

Requires Node.js ≥ 22.5 (uses the built-in `node:sqlite`). The only npm dependency is
`jpeg-js` (pure JS, no native build).

## What's implemented (spec P0)

**Product flow** — Onboarding (3 pages) → Discover (hero exhibition + shelves) →
Exhibition → Style detail sheet → Photo import (library/camera, client-side JPEG
re-encode strips EXIF/GPS) → "Reading the image" analysis → Styles (recommendations
with reason codes + theme groups + subject filters) → Preview settings (strength /
fidelity / composition / ratio) → Generation progress (4 honest stages, leave-and-return) →
Result (hold-to-see-original, before/after compare slider, save, share, feedback) →
Refine / Try again → Projects → Profile → Paywall.

**Style system (§11)** — 24 original StyleSpecs in 6 curated exhibitions
(Quiet Portraits, Printed Matter, Dream Geography, Graphic Light, Material Studies,
Small Cinemas). Each spec is a versioned JSON asset with identity, intent,
compatibility, controls, and a pixel `pipeline`. Published StyleVersions are immutable.

**Generation (§9)** — `LocalStyleEngine` Model Adapter: decode → composition/ratio
crop → style pipeline (split-tone grade, duotone/tritone, palette map, posterize,
halftone, weave, paper grain, bloom, chroma offset, vignette…) scaled by *strength*,
identity blend by *fidelity* → quality gate (decodable, non-blank, sane dimensions) →
one automatic retry → candidate asset. Worker queue survives restarts.

**Billing (§13)** — append-only credit ledger with `reserve → commit / release`,
earliest-expiring bucket first, unique reference keys; free first image; mock store
(`/v1/purchases/verify`) with Mini Pack / Creator Monthly / Creator Annual; premium
style gating; paywall shown after the free save, never before value.

**API (§10)** — `/v1/auth/exchange` (guest-first), `/v1/discover`, `/v1/styles`,
upload intents → binary PUT → idempotent complete, `/v1/assets/:id/analysis`
(heuristic subject/exposure/sharpness adapter + ranked recommendations), projects CRUD,
`/v1/generation-jobs` (Idempotency-Key required), cancel, feedback, export,
entitlements, products, purchases, `/v1/events` telemetry. Stable error codes
(`INSUFFICIENT_ENTITLEMENT`, `ASSET_UNSUPPORTED`, …) with `requestId`.

## Verified acceptance scenarios (spec §24)

- Free first image: one reserve/commit pair, balance 0, no watermark, paywall after save.
- Duplicate Idempotency-Key → same job id, single reserve.
- Two concurrent jobs racing the last unit → exactly one succeeds, other gets 402.
- Failed/rejected generations release the reserve (0 units charged).
- Failed retry never hides an earlier successful work in Projects.
- Premium style on free plan → 402 with paywall context; unlocked by Creator.

## Layout

```
server/
  index.js     HTTP server + static hosting
  api.js       /v1 route handlers
  db.js        node:sqlite schema (spec §12, adapted)
  ledger.js    append-only credit ledger (§13.4)
  jobs.js      generation worker + quality gate (§9.2/9.4)
  styles.js    24 StyleSpecs · 6 exhibitions · product catalog
  engine/      jpeg codec wrapper, pixel ops, pipeline interpreter, analysis
web/
  index.html · app.css · api.js · app.js   (no-build mobile-first SPA)
data/          SQLite DB + uploaded/generated assets (created at runtime)
```

## Model adapters (spec §9.3 primary + backup)

- **Primary — RemoteImageAdapter** (`server/engine/remoteAdapter.js`): calls an
  OpenAI-compatible `/v1/images/edits` endpoint (image-to-image) with the source
  photo and an instruction assembled from the StyleSpec's `promptAssembly`
  (original baseDirection per style + subject rules from analysis + control
  fragments for strength/fidelity/composition + negative constraints). Sources
  are downscaled to 1024 px before sending; results are center-cropped to the
  requested output ratio. Configure via `.env` (`IMAGE_PROVIDER=remote`,
  base URL / API key / model, default `gpt-image-2`). Typical latency 1–5 min.
- **Backup — LocalStyleEngine**: the deterministic pixel engine. Used
  automatically when the provider fails or times out (safety rejections are NOT
  retried locally — the job fails with `GENERATION_REJECTED` and 0 units charged),
  and exclusively when `IMAGE_PROVIDER=local` or no key is set.

## Performance notes (provider latency)

The provider's `/v1/images/edits` latency is queue-driven and varies widely
(40 s – 300 s+). Measured findings: the `quality` parameter does **not** reduce
latency; input downscaling to 1024 px helps modestly. Mitigations in place:
- worker concurrency 3 (`WORKER_CONCURRENCY`) so jobs don't serialize,
- 420 s provider timeout so slow generations finish instead of failing,
- honest 1–5 min estimates surfaced before Generate and on the progress screen,
- progress screen supports leaving and returning (Projects shows live status),
- automatic local-engine fallback if the provider errors or times out.

## Style-card samples (spec §5.1)

Cards show real generated samples from `web/covers/{internal_key}.jpg`
(gradient placeholder is only a fallback). Regenerate:

```bash
node server/tools/gen-samples.js               # local engine, all missing, instant
node server/tools/gen-samples.js --remote all  # provider samples, ~2–4 min each
```

## Small Press exhibition (community-adapted styles)

Six styles adapted from MIT-licensed community skills (source and license
recorded in each StyleSpec's `provenance`; local clones in `.skills/`):
- **Zine Poster** — `LiamGvchi/gc-minimal-zine-poster`: warm scanned-paper
  field, dominant negative space, the photo re-entering as torn halftone
  fragments with one saturated accent and sparse typewriter annotations.
- **Cover Story** — `dacnay816y62-hub/fantasy-qiqiguaiguai-skill` (T1 Portrait
  Editorial): identity-preserving witty magazine-cover treatment with an
  invented generic masthead and a restrained 2–4 color palette.
- **Reportage Wash** — `serenashenn3-art/watercolor-sketch-style`: news-sketch
  ink lines + transparent watercolor washes, pastel palette, handwritten scene
  notes (no signatures/dates).
- **Ink & Seal** — `sammyteng/illustration-studio` (oriental-ink-guofeng):
  ink-wash rebuild on rice paper, gongbi contour lines, one cinnabar seal as
  the only saturated accent.
- **One Line** — `sammyteng/illustration-studio` (editorial-line): single
  continuous ink contour + one muted accent, New-Yorker-adjacent restraint.
- **Studio Hour** (premium) — `ZoeZYZY/go-photo-studio-skill`: identity-locked
  executive studio headshot (lighting/backdrop/attire upgrade, never a
  different person).

Evaluated and **rejected**: `HyperfocuSam/style-parody-poster` (recreates
existing posters/ads in their exact style — brand-mimicry risk, spec §1.4).

These styles carry their own `negativeConstraints` (designed text is allowed
where the style calls for it; real brands/logos never are).

**Prompt compiler for designed styles.** Static prompts can't reproduce what
these skills actually do — they have an LLM design each poster around the
specific photo. `server/engine/promptCompiler.js` ports that step: before
generation, a fast vision-capable chat model (`PROMPT_COMPILER_MODEL`, default
`gpt-5.4-mini`) looks at the photo and compiles a four-paragraph image-edit
prompt — fragment/layout plan matched to the scene's actual layers, short
content-derived annotation texts, the color-anchor choice, and orientation
invariants (never rotate/flip source fragments). Falls back to the static
`promptAssembly` if the call fails. Enabled per style via
`promptAssembly.compiler: "zine" | "editorial"`.

## Deliberate MVP simplifications

- Analysis is heuristic (no face detection); labeled `heuristic-0.1`.
- Store purchases are mocked server-side with the real verify/grant shape.
- Uploads are session-authenticated paths standing in for short-lived signed URLs.
- Apple/Google sign-in uses a dev fake-provider adapter; guest-first works fully.
- Per-job cost telemetry stores provider token usage as a proxy metric.
