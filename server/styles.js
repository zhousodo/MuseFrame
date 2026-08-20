// MuseFrame style catalog — 24 original StyleSpecs in 6 curated exhibitions (spec §11.3).
// Every style is a versioned, structured asset: identity, intent, compatibility,
// controls and a pixel `pipeline` interpreted by the LocalStyleEngine adapter.
// Names/captions are original working names per spec §11.1 (no third-party prompts).

const G = 'repeating-linear-gradient(115deg,rgba(23,23,23,.03) 0 2px,rgba(255,255,255,.02) 2px 4px),';

function style(internalKey, name, caption, tags, premium, coverArt, compat, pipeline) {
  return {
    internalKey, name, caption, tags, premium,
    coverArt: G + coverArt,
    compatibility: compat, // {person, pet, landscape, object}
    pipeline,              // ordered ops for the local (backup) engine
  };
}

// Original clean-room baseDirections for the remote model adapter (spec §11.2
// promptAssembly). Keyed by internalKey; shared rules/fragments live in
// PROMPT_COMMON and are assembled per request by the adapter.
export const BASE_DIRECTIONS = {
  quiet_soft_window_01: 'Transform this photograph into a quiet portrait treatment bathed in soft directional window light: gentle contrast, muted warm neutrals, creamy unblown highlights, a calm airy mood, and fine photographic grain.',
  quiet_muted_room_01: 'Transform this photograph into a quiet interior treatment: a desaturated palette of clay, dust and putty tones, soft even light, matte finish, and restrained shadow depth.',
  quiet_silver_air_01: 'Transform this photograph into a cool silver monochrome with the faintest blue cast: fine grain, luminous airy highlights, and delicate tonal transitions like a silver gelatin print.',
  quiet_still_gaze_01: 'Transform this photograph into a held, museum-print portrait: deep neutral ground, restrained warm-grey palette, a strong quiet vignette concentrating attention on the subject, matte surface.',
  print_ink_register_01: 'Transform this photograph into an editorial offset-print treatment: a limited ink palette of deep blue, warm red and paper cream, a slight ink registration offset at contour edges, visible uncoated paper grain, and flat graphic ink layers.',
  print_paper_cut_01: 'Transform this photograph into a flat layered cut-paper collage: crisp blade-cut shapes, a small solid palette of terracotta, deep green, cream and mustard, with subtle paper-edge shadows.',
  print_dry_press_01: 'Transform this photograph into a chalky letterpress monochrome: dry pressed-ink blacks, warm paper whites, and a coarse tactile pressed-paper texture.',
  print_sunday_insert_01: 'Transform this photograph into a vintage newspaper color-supplement print: visible halftone dots, warm cream paper, cheerful slightly-oversaturated primaries, and a soft ink bleed.',
  dream_mist_atlas_01: 'Transform this photograph into a fog-layered atmosphere: pale grey-blue palette, long-exposure calm, soft light gradients, and detail dissolving gently into the distance.',
  dream_night_orchard_01: 'Transform this photograph into a deep blue night scene warmed by lantern glow: quiet darkness, luminous amber highlights, and gentle photographic grain.',
  dream_blue_distance_01: 'Transform this photograph into an atmospheric haze that deepens with distance: airy blue gradient, soft cool light, and a sense of open space.',
  dream_floating_weather_01: 'Transform this photograph into lifted pastel weather: pale lavender and grey sky tones, weightless soft masses, and a dreamlike lightness.',
  graphic_cobalt_shadow_01: 'Transform this photograph into a bold graphic treatment ruled by one cobalt blue cut with hard shadow: a duotone of deep blue and pale paper, decisive flat shapes, minimal texture.',
  graphic_hard_noon_01: 'Transform this photograph into harsh noon light: bleached pale ground, crisp deep shadows with hard edges, high graphic contrast, minimal midtones.',
  graphic_color_block_01: 'Transform this photograph into a poster of three flat color fields — vermilion, cream and deep blue — organizing the frame around the subject with clean flat edges.',
  graphic_afterimage_01: 'Transform this photograph into a retro optical print with a chromatic echo: subtle red and blue separation at contour edges, warm beige midtones, and a violet tint in the shadows.',
  material_woven_light_01: 'Transform this photograph so warm light appears carried through visible woven fabric: a fine textile weave across the surface, honey and flax palette, soft tactile warmth.',
  material_mineral_wash_01: 'Transform this photograph into a stone pigment wash: cool sediment greys and green-greys, granular watercolor-on-plaster texture, matte mineral finish.',
  material_wax_surface_01: 'Transform this photograph into a sealed wax surface like an encaustic painting: softened edges, warm translucent glowing highlights, smooth luminous finish.',
  material_charcoal_bloom_01: 'Transform this photograph into an ash-black charcoal drawing with a breathing luminous core: visible charcoal strokes, paper tooth, and a strong quiet vignette.',
  cinema_quiet_frame_01: 'Transform this photograph into a muted teal-and-sand film still: patient framing, soft filmic contrast, faint grain, and an understated cinematic color grade.',
  cinema_late_platform_01: 'Transform this photograph into a late-night station film still: sodium-amber highlights against cool darkness, quiet tension, and cinematic grain.',
  cinema_warm_interior_01: 'Transform this photograph into a tungsten-lit interior film scene: honest warm skin tones, an amber-brown palette, gentle contrast, and filmic texture.',
  cinema_distant_flash_01: 'Transform this photograph into a cold direct-flash night snapshot: a brightly lit subject against a darkness that falls off quickly, slight vignette, candid raw feel.',
};

// Community-adapted directions (MIT-licensed skills, reworded for MuseFrame;
// provenance recorded per spec §14.4). These two invite designed text into the
// frame, so they carry their own negativeConstraints instead of the global ones.
BASE_DIRECTIONS.press_zine_poster_01 = 'Recompose this photograph as a poetic minimal zine poster: a flat scanned warm-paper field with visible fibers, grain and scan noise, where most of the canvas stays open paper. Convert the photograph into one small editorial event — torn or cut printed fragments, layered strips or a cutout of the source image in soft halftone monochrome — while the main subject stays clearly recognizable and may keep its true colors as the poster’s single saturated accent. Add only a few sparse typewriter-style annotations, like tiny archival labels, measurements or a short quiet phrase. Flat orthographic scan look, diffuse light, no hard shadows; risograph and halftone reproduction with slight misregistration and torn paper edges. Quiet, archival, diary-like mood — never a commercial ad.';
BASE_DIRECTIONS.press_cover_story_01 = 'Redesign this photograph as a playful editorial magazine cover. The subject remains the absolute visual center; build a bold cover hierarchy around them: one large high-contrast invented masthead word, two or three short witty cover lines as smaller labels, and minimal decorative marks. Use a restrained palette of two to four colors that flatters the photograph, magazine-grade lighting, and a clean print texture. The mood is lighthearted, wry, fashion-adjacent social editorial.';
BASE_DIRECTIONS.press_reportage_wash_01 = 'Redraw this photograph as a reportage sketch illustration — a news sketch artist’s hand-drawn ink and watercolor on paper. Loose black ink pen outlines with visible sketchy strokes, contour lines not fully closed, occasional searching repeat lines. Transparent watercolor washes layered over the ink: thin color layers with visible paper texture and softly bleeding edges. Pastel palette of pale yellow, dusty pink, lavender, sky blue, warm wood brown and rosy skin tones. The background becomes a soft monochrome wash with generous white paper. Figures stay recognizable but slightly loosened in a lively reportage manner — captured gestures, on-the-spot documentary feel, observer’s viewpoint. A few handwritten-style scene annotations may label the scene.';
BASE_DIRECTIONS.press_ink_seal_01 = 'Repaint this photograph in East Asian ink-wash language on rice paper: a warm paper-white ground with fine fiber texture where at least half the frame stays empty. The subject is rebuilt from ink wash — five tones of ink from wet dark pools to dry flying-white strokes, with naturally bleeding water edges — steadied by a few fine even brush contour lines. Add restrained touches of traditional pigment (indigo blue, ochre brown, dark crimson) in one or two places only, translucent over the ink. Finish with exactly one small cinnabar-red seal stamp in a corner — the single saturated accent in the image. No western shadows or highlights; light lives in the ink’s density. Composition follows the ideal of treating emptiness as substance: one main subject, generous silence around it.';
BASE_DIRECTIONS.press_one_line_01 = 'Reduce this photograph to a minimalist single-continuous-line illustration with editorial-magazine sophistication: the subject redrawn as ONE elegant thin black ink contour with very few pen lifts, on a near-white ground with a faint paper feel. The line stays confident and economical while keeping the subject’s pose, proportions and defining features recognizable. Use exactly one muted accent color (brick red, ochre, indigo or olive) in one or two small areas. Vast negative space; calm, intellectual, timeless.';
BASE_DIRECTIONS.press_studio_hour_01 = 'Remake this photograph as an executive studio portrait: the person is placed against a neutral soft-gradient studio backdrop with soft-box lighting, balanced head-and-shoulders composition, and clean professional attire adapted from their actual clothing. Identity anchors are locked: face geometry, eye/nose/mouth proportions, apparent age, skin micro-texture, hairstyle and expression stay exactly as photographed — this is a lighting and setting upgrade, never a different person.';

export const STYLE_NEGATIVES = {
  press_zine_poster_01: [
    'Keep every source fragment in its original orientation — never rotate, flip or mirror the photograph or its parts; any visible source text stays upright and unmirrored.',
    'Keep all text minimal and small — a few short typewriter labels at most; no headlines, no paragraphs.',
    'Do not use real brand names, logos, watermarks or signatures.',
    'Do not fill the canvas — most of it must remain open paper.',
    'Do not change the number of people or the subject’s identity.',
  ],
  press_cover_story_01: [
    'Invented cover text must be short, generic words — never a real magazine name, brand or logo.',
    'Do not change the subject’s identity, age, gender expression, skin tone, hairstyle, outfit or pose.',
    'Do not change the number of people.',
    'No more than a handful of short text elements; keep small text subordinate to the masthead.',
  ],
  press_reportage_wash_01: [
    'No photorealism, no 3D render, no smooth digital gradients, no anime, no oil painting.',
    'No artist signature, no dates, no timestamps anywhere in the image.',
    'Keep the number of people and their recognizability unchanged.',
    'Keep annotations short and handwritten in style; no headlines or paragraphs.',
  ],
  press_ink_seal_01: [
    'Exactly one red seal stamp — no other saturated red, no second seal, no readable calligraphy text.',
    'No western-style shadows, volumetric light or highlights; no photographic realism.',
    'The ground must read as rice paper, not pure white, not a gradient.',
    'Keep the subject recognizable; do not change the number of people or animals.',
  ],
  press_one_line_01: [
    'One continuous line only — no crosshatching, no messy sketch buildup, no filled shading.',
    'At most one accent color; no additional colors.',
    'No cartoon or cute styling, no big titles, no infographic elements.',
    'Keep the subject’s pose and proportions recognizable.',
  ],
  press_studio_hour_01: [
    'Never alter face geometry, age, gender expression, ethnicity, skin tone or skin texture — no beautifying, slimming or de-aging.',
    'No waxy or plastic skin smoothing, no fashion-magazine over-retouching.',
    'Do not change the number of people; no face swap.',
    'No text, watermarks or logos.',
  ],
};

export const PROMPT_COMMON = {
  subjectRules: {
    person: 'Preserve the person’s identity exactly: facial structure, expression, pose, hairstyle, apparent age, skin tone, and any glasses or accessories. Do not beautify, slim, or otherwise alter the person.',
    pet: 'Preserve the animal’s breed, markings, proportions and pose exactly.',
    landscape: 'Preserve the scene’s layout, landmarks and horizon exactly.',
    object: 'Preserve the object’s geometry, proportions and primary silhouette exactly.',
  },
  strength: {
    soft: 'Apply the treatment gently — the original photograph should remain clearly recognizable at first glance.',
    balanced: 'Apply the treatment with confident, balanced intensity.',
    bold: 'Commit fully to the treatment with strong, unmistakable stylization.',
  },
  fidelity: {
    high: 'Keep fine detail and proportions strictly faithful to the source image.',
    natural: 'Slight artistic license in texture is allowed, but the subject must stay clearly recognizable.',
  },
  composition: {
    keep: 'Keep the original framing and composition.',
    reframe: 'You may recompose slightly for a stronger frame, keeping the subject dominant.',
  },
  negativeConstraints: [
    'Do not add text, watermarks, signatures, logos or borders.',
    'Do not change the number of people or animals.',
    'Do not add new objects, people or scenery that are not in the source.',
  ],
};

export const THEMES = [
  {
    slug: 'quiet-portraits', title: 'Quiet Portraits', edition: 'EDITION 01',
    note: 'Soft light, restrained color, and room to breathe.',
    styles: [
      style('quiet_soft_window_01', 'Soft Window', 'Near-window light, gentle contrast.', ['portrait'], false,
        'radial-gradient(120% 80% at 25% 12%,rgba(247,240,224,.9) 0%,rgba(247,240,224,0) 55%),linear-gradient(155deg,#E3D9C5 0%,#B9AD97 55%,#8A806E 100%)',
        { person: .95, pet: .7, landscape: .4, object: .5 },
        [
          ['soften', { radius: 2, mix: .35 }],
          ['curve', { contrast: -.12, brightness: .06, saturation: -.22, gamma: .96 }],
          ['grade', { shadows: [26, 22, 12], highs: [255, 248, 232], amount: .5 }],
          ['fade', { lift: .07 }],
          ['grain', { amount: .05 }],
        ]),
      style('quiet_muted_room_01', 'Muted Room', 'Quiet interiors, clay and dust tones.', ['portrait', 'object'], false,
        'linear-gradient(160deg,#D8D2C6 0%,#A8A196 55%,#777166 100%)',
        { person: .9, pet: .7, landscape: .5, object: .8 },
        [
          ['curve', { contrast: -.08, brightness: .02, saturation: -.38, gamma: 1 }],
          ['grade', { shadows: [46, 40, 34], mids: [168, 158, 142], highs: [236, 229, 214], amount: .55 }],
          ['fade', { lift: .05 }],
          ['grain', { amount: .06 }],
          ['vignette', { amount: .16 }],
        ]),
      style('quiet_silver_air_01', 'Silver Air', 'Cool silver cast, fine grain.', ['portrait'], true,
        'linear-gradient(170deg,#EBECEC 0%,#C0C5C8 50%,#8F979D 100%)',
        { person: .95, pet: .75, landscape: .6, object: .6 },
        [
          ['curve', { contrast: .05, brightness: .04, saturation: -.85, gamma: .95 }],
          ['grade', { shadows: [30, 36, 42], highs: [240, 246, 250], amount: .6 }],
          ['grain', { amount: .1 }],
          ['bloom', { radius: 6, mix: .18 }],
        ]),
      style('quiet_still_gaze_01', 'Still Gaze', 'Held expression, deep neutral ground.', ['portrait'], false,
        'radial-gradient(80% 60% at 50% 35%,rgba(217,207,194,.55) 0%,rgba(0,0,0,0) 60%),linear-gradient(150deg,#C9BFB2 0%,#8F8275 55%,#4E463D 100%)',
        { person: .95, pet: .6, landscape: .3, object: .5 },
        [
          ['curve', { contrast: .16, brightness: -.05, saturation: -.3, gamma: 1.06 }],
          ['grade', { shadows: [22, 19, 15], highs: [235, 226, 210], amount: .5 }],
          ['vignette', { amount: .34 }],
          ['grain', { amount: .07 }],
        ]),
    ],
  },
  {
    slug: 'printed-matter', title: 'Printed Matter', edition: 'EDITION 01',
    note: 'Ink, paper grain, and the discipline of the press.',
    styles: [
      style('print_ink_register_01', 'Ink Register', 'Offset inks on uncoated paper.', ['portrait', 'object'], false,
        'linear-gradient(145deg,#F1EEE4 0%,#F1EEE4 44%,#22335F 44%,#22335F 72%,#B4432E 72%,#B4432E 100%)',
        { person: .9, pet: .7, landscape: .6, object: .9 },
        [
          ['posterize', { levels: 5, amount: .7 }],
          ['tritone', { dark: [30, 42, 84], mid: [166, 84, 62], light: [241, 238, 228], amount: .78 }],
          ['chromaOffset', { shift: 2, amount: .5 }],
          ['paper', { amount: .5 }],
        ]),
      style('print_paper_cut_01', 'Paper Cut', 'Flat shapes with a clean blade edge.', ['object', 'portrait'], false,
        'linear-gradient(35deg,#E8E2D2 0%,#E8E2D2 52%,#C9563C 52%,#C9563C 82%,#2E3A2F 82%)',
        { person: .7, pet: .6, landscape: .7, object: .9 },
        [
          ['soften', { radius: 1, mix: .5 }],
          ['posterize', { levels: 4, amount: .9 }],
          ['palette', { colors: [[36, 46, 38], [201, 86, 60], [232, 226, 210], [94, 110, 92], [222, 178, 92]], amount: .72 }],
          ['paper', { amount: .35 }],
        ]),
      style('print_dry_press_01', 'Dry Press', 'Chalky blacks, pressed texture.', ['portrait'], false,
        'linear-gradient(160deg,#EFEAD9 0%,#CFC6AC 45%,#6E6753 100%)',
        { person: .9, pet: .75, landscape: .5, object: .8 },
        [
          ['curve', { contrast: .2, brightness: .02, saturation: -.9, gamma: 1.02 }],
          ['duotone', { dark: [58, 54, 44], light: [239, 234, 217], amount: .85 }],
          ['grain', { amount: .14 }],
          ['paper', { amount: .6 }],
        ]),
      style('print_sunday_insert_01', 'Sunday Insert', 'Halftone warmth, weekend color.', ['portrait', 'object'], true,
        'linear-gradient(120deg,#F0E7D2 0%,#F0E7D2 38%,#2E4A9E 38%,#2E4A9E 70%,#D9A441 70%)',
        { person: .85, pet: .8, landscape: .6, object: .85 },
        [
          ['curve', { contrast: .1, brightness: .04, saturation: .18, gamma: 1 }],
          ['halftone', { cell: 4, amount: .5 }],
          ['grade', { shadows: [40, 52, 110], highs: [240, 231, 210], amount: .4 }],
          ['paper', { amount: .4 }],
        ]),
    ],
  },
  {
    slug: 'dream-geography', title: 'Dream Geography', edition: 'EDITION 01',
    note: 'Landscapes remembered slightly wrong.',
    styles: [
      style('dream_mist_atlas_01', 'Mist Atlas', 'Fog layers over slow water.', ['landscape'], false,
        'linear-gradient(180deg,#DFE5E6 0%,#AABBC0 55%,#748A92 100%)',
        { person: .5, pet: .5, landscape: .95, object: .5 },
        [
          ['bloom', { radius: 10, mix: .4 }],
          ['curve', { contrast: -.2, brightness: .08, saturation: -.35, gamma: .93 }],
          ['grade', { shadows: [70, 84, 90], highs: [226, 232, 233], amount: .55 }],
          ['fade', { lift: .12 }],
        ]),
      style('dream_night_orchard_01', 'Night Orchard', 'Blue night, lantern warmth.', ['landscape', 'portrait'], true,
        'radial-gradient(60% 40% at 70% 70%,rgba(217,164,65,.35) 0%,rgba(0,0,0,0) 60%),linear-gradient(175deg,#2A3440 0%,#1B2430 60%,#10151D 100%)',
        { person: .7, pet: .6, landscape: .9, object: .6 },
        [
          ['curve', { contrast: .12, brightness: -.16, saturation: -.1, gamma: 1.14 }],
          ['grade', { shadows: [16, 22, 38], mids: [52, 66, 92], highs: [217, 170, 96], amount: .7 }],
          ['vignette', { amount: .4 }],
          ['bloom', { radius: 8, mix: .22 }],
          ['grain', { amount: .08 }],
        ]),
      style('dream_blue_distance_01', 'Blue Distance', 'Haze that deepens with distance.', ['landscape', 'portrait'], false,
        'linear-gradient(180deg,#CBD9E8 0%,#7E9CC0 55%,#41608C 100%)',
        { person: .75, pet: .6, landscape: .95, object: .5 },
        [
          ['curve', { contrast: -.05, brightness: .03, saturation: -.25, gamma: .97 }],
          ['grade', { shadows: [40, 62, 104], mids: [116, 142, 178], highs: [226, 234, 244], amount: .62 }],
          ['fade', { lift: .08 }],
          ['bloom', { radius: 6, mix: .18 }],
        ]),
      style('dream_floating_weather_01', 'Floating Weather', 'Lifted pastel skies, soft mass.', ['landscape'], false,
        'linear-gradient(200deg,#EBE6F0 0%,#B9B4CE 55%,#837DA6 100%)',
        { person: .6, pet: .6, landscape: .9, object: .55 },
        [
          ['soften', { radius: 3, mix: .3 }],
          ['curve', { contrast: -.16, brightness: .1, saturation: -.12, gamma: .92 }],
          ['grade', { shadows: [122, 116, 152], highs: [240, 235, 243], amount: .5 }],
          ['fade', { lift: .12 }],
        ]),
    ],
  },
  {
    slug: 'graphic-light', title: 'Graphic Light', edition: 'EDITION 01',
    note: 'Hard light, flat color, decisive shadows.',
    styles: [
      style('graphic_cobalt_shadow_01', 'Cobalt Shadow', 'One blue, cut by hard shadow.', ['portrait', 'architecture'], false,
        'linear-gradient(135deg,#1C49D8 0%,#12308F 58%,#0A1C52 100%)',
        { person: .85, pet: .6, landscape: .7, object: .8 },
        [
          ['curve', { contrast: .34, brightness: -.02, saturation: -.5, gamma: 1.05 }],
          ['duotone', { dark: [10, 28, 82], light: [232, 237, 255], amount: .8 }],
          ['posterize', { levels: 6, amount: .35 }],
          ['grain', { amount: .05 }],
        ]),
      style('graphic_hard_noon_01', 'Hard Noon', 'Noon contrast, bleached ground.', ['architecture', 'landscape'], false,
        'linear-gradient(120deg,#F5EFDF 0%,#F5EFDF 54%,#17233F 54%)',
        { person: .65, pet: .5, landscape: .85, object: .8 },
        [
          ['curve', { contrast: .42, brightness: .06, saturation: -.3, gamma: 1.1 }],
          ['grade', { shadows: [23, 35, 63], highs: [245, 239, 223], amount: .5 }],
          ['posterize', { levels: 7, amount: .25 }],
        ]),
      style('graphic_color_block_01', 'Color Block', 'Three fields, one subject.', ['portrait'], true,
        'linear-gradient(90deg,#D8532F 0%,#D8532F 33%,#EFE8D6 33%,#EFE8D6 66%,#22408F 66%)',
        { person: .85, pet: .7, landscape: .5, object: .8 },
        [
          ['soften', { radius: 1, mix: .4 }],
          ['posterize', { levels: 4, amount: .85 }],
          ['palette', { colors: [[216, 83, 47], [239, 232, 214], [34, 64, 143], [24, 24, 26], [222, 172, 60]], amount: .68 }],
          ['grain', { amount: .04 }],
        ]),
      style('graphic_afterimage_01', 'Afterimage', 'Chromatic echo at the edges.', ['portrait'], false,
        'linear-gradient(140deg,#E5DCC8 0%,#C87B4E 50%,#5A3E8E 100%)',
        { person: .85, pet: .65, landscape: .6, object: .7 },
        [
          ['chromaOffset', { shift: 5, amount: .8 }],
          ['curve', { contrast: .14, brightness: 0, saturation: .16, gamma: 1 }],
          ['grade', { shadows: [70, 50, 110], highs: [235, 222, 200], amount: .38 }],
          ['grain', { amount: .07 }],
        ]),
    ],
  },
  {
    slug: 'material-studies', title: 'Material Studies', edition: 'EDITION 01',
    note: 'Surfaces you can almost touch: weave, mineral, wax and ash.',
    styles: [
      style('material_woven_light_01', 'Woven Light', 'Fabric weave carrying warm light.', ['object', 'pet', 'portrait'], false,
        'linear-gradient(150deg,#E9DFC8 0%,#C9B48C 50%,#8F7B58 100%)',
        { person: .75, pet: .8, landscape: .6, object: .9 },
        [
          ['weave', { cell: 3, amount: .4 }],
          ['curve', { contrast: .04, brightness: .04, saturation: -.15, gamma: .98 }],
          ['grade', { shadows: [74, 60, 38], highs: [240, 230, 205], amount: .5 }],
          ['grain', { amount: .06 }],
        ]),
      style('material_mineral_wash_01', 'Mineral Wash', 'Stone pigment, cool sediment.', ['object', 'landscape'], false,
        'linear-gradient(165deg,#D7DBD4 0%,#9FAAA2 50%,#5E6D68 100%)',
        { person: .6, pet: .65, landscape: .85, object: .9 },
        [
          ['curve', { contrast: .06, brightness: 0, saturation: -.45, gamma: 1.02 }],
          ['grade', { shadows: [52, 66, 62], mids: [136, 148, 140], highs: [226, 230, 222], amount: .6 }],
          ['paper', { amount: .45 }],
          ['grain', { amount: .09 }],
        ]),
      style('material_wax_surface_01', 'Wax Surface', 'Sealed glow, softened edges.', ['object', 'portrait'], false,
        'linear-gradient(155deg,#F2E7D4 0%,#DDC49E 55%,#A98D66 100%)',
        { person: .8, pet: .7, landscape: .5, object: .9 },
        [
          ['soften', { radius: 3, mix: .45 }],
          ['bloom', { radius: 7, mix: .3 }],
          ['curve', { contrast: -.06, brightness: .05, saturation: -.08, gamma: .95 }],
          ['grade', { shadows: [96, 74, 48], highs: [246, 236, 218], amount: .45 }],
        ]),
      style('material_charcoal_bloom_01', 'Charcoal Bloom', 'Ash blacks with a breathing core.', ['portrait', 'object'], false,
        'radial-gradient(70% 55% at 50% 40%,rgba(214,206,196,.5) 0%,rgba(0,0,0,0) 60%),linear-gradient(160deg,#9A948C 0%,#4E4A45 55%,#211F1D 100%)',
        { person: .85, pet: .75, landscape: .6, object: .85 },
        [
          ['curve', { contrast: .22, brightness: -.06, saturation: -1, gamma: 1.08 }],
          ['duotone', { dark: [24, 23, 22], light: [226, 220, 211], amount: .9 }],
          ['vignette', { amount: .36 }],
          ['grain', { amount: .16 }],
          ['bloom', { radius: 9, mix: .14 }],
        ]),
    ],
  },
  {
    slug: 'small-cinemas', title: 'Small Cinemas', edition: 'EDITION 01',
    note: 'Stills from films that were never made.',
    styles: [
      style('cinema_quiet_frame_01', 'Quiet Frame', 'Muted teal air, patient framing.', ['portrait', 'street'], false,
        'linear-gradient(170deg,#C7D2CE 0%,#7E958F 55%,#44585A 100%)',
        { person: .9, pet: .7, landscape: .8, object: .7 },
        [
          ['curve', { contrast: .08, brightness: -.02, saturation: -.28, gamma: 1.02 }],
          ['grade', { shadows: [36, 52, 54], mids: [110, 128, 124], highs: [232, 228, 214], amount: .6 }],
          ['fade', { lift: .06 }],
          ['grain', { amount: .09 }],
          ['vignette', { amount: .2 }],
        ]),
      style('cinema_late_platform_01', 'Late Platform', 'Sodium lamps, last train tension.', ['street', 'portrait'], false,
        'radial-gradient(55% 40% at 30% 30%,rgba(226,178,94,.4) 0%,rgba(0,0,0,0) 60%),linear-gradient(170deg,#4A4436 0%,#2C2A26 55%,#191817 100%)',
        { person: .8, pet: .55, landscape: .75, object: .6 },
        [
          ['curve', { contrast: .18, brightness: -.12, saturation: -.15, gamma: 1.12 }],
          ['grade', { shadows: [26, 24, 26], mids: [96, 82, 56], highs: [226, 182, 108], amount: .68 }],
          ['vignette', { amount: .42 }],
          ['grain', { amount: .12 }],
        ]),
      style('cinema_warm_interior_01', 'Warm Interior', 'Tungsten rooms, honest faces.', ['portrait'], false,
        'linear-gradient(160deg,#E8CFA8 0%,#C29B6C 50%,#7D6141 100%)',
        { person: .95, pet: .8, landscape: .4, object: .7 },
        [
          ['curve', { contrast: .06, brightness: .02, saturation: -.1, gamma: .99 }],
          ['grade', { shadows: [64, 44, 30], mids: [180, 140, 96], highs: [248, 232, 202], amount: .55 }],
          ['fade', { lift: .05 }],
          ['grain', { amount: .08 }],
          ['vignette', { amount: .24 }],
        ]),
      style('cinema_distant_flash_01', 'Distant Flash', 'Cold flash, caught mid-thought.', ['portrait', 'street'], true,
        'linear-gradient(200deg,#E6E4E6 0%,#9E9FAB 45%,#4C4E5C 100%)',
        { person: .9, pet: .7, landscape: .5, object: .65 },
        [
          ['curve', { contrast: .3, brightness: .08, saturation: -.4, gamma: 1.06 }],
          ['grade', { shadows: [40, 42, 56], highs: [240, 240, 244], amount: .5 }],
          ['vignette', { amount: .48 }],
          ['chromaOffset', { shift: 2, amount: .4 }],
          ['grain', { amount: .1 }],
        ]),
    ],
  },
  {
    slug: 'small-press', title: 'Small Press', edition: 'EDITION 02',
    note: 'Community-loved treatments, reissued as gallery editions.',
    styles: [
      style('press_zine_poster_01', 'Zine Poster', 'Torn paper, one loud color, quiet notes.', ['portrait', 'landscape', 'object'], false,
        'radial-gradient(70% 50% at 62% 40%,rgba(28,73,216,.6) 0%,rgba(0,0,0,0) 45%),linear-gradient(170deg,#F2EDE0 0%,#EAE3D2 55%,#D8CFBA 100%)',
        { person: .85, pet: .8, landscape: .9, object: .9 },
        [
          ['duotone', { dark: [58, 54, 46], light: [242, 237, 224], amount: .8 }],
          ['halftone', { cell: 3, amount: .45 }],
          ['paper', { amount: .7 }],
          ['fade', { lift: .08 }],
        ]),
      style('press_cover_story_01', 'Cover Story', 'You, on this month’s witty cover.', ['portrait', 'pet'], false,
        'linear-gradient(160deg,#F5E9DC 0%,#F5E9DC 30%,#D8532F 30%,#D8532F 38%,#F5E9DC 38%,#E8D9C4 70%,#8E6E4E 100%)',
        { person: .95, pet: .9, landscape: .3, object: .5 },
        [
          ['curve', { contrast: .12, brightness: .04, saturation: .12, gamma: .98 }],
          ['grade', { shadows: [60, 44, 36], highs: [248, 238, 222], amount: .4 }],
          ['bloom', { radius: 6, mix: .2 }],
          ['grain', { amount: .04 }],
        ]),
      style('press_reportage_wash_01', 'Reportage Wash', 'Ink lines, watercolor air, field notes.', ['portrait', 'street', 'object'], false,
        'radial-gradient(80% 55% at 40% 35%,rgba(214,182,214,.5) 0%,rgba(0,0,0,0) 55%),linear-gradient(165deg,#F6F1E2 0%,#EBDFC8 55%,#CBD8E4 100%)',
        { person: .9, pet: .85, landscape: .8, object: .85 },
        [
          ['soften', { radius: 2, mix: .5 }],
          ['posterize', { levels: 6, amount: .4 }],
          ['grade', { shadows: [96, 88, 120], mids: [212, 188, 170], highs: [248, 243, 226], amount: .5 }],
          ['fade', { lift: .1 }],
          ['paper', { amount: .5 }],
        ]),
      style('press_ink_seal_01', 'Ink & Seal', 'Five tones of ink, one red seal.', ['landscape', 'object', 'portrait'], false,
        'radial-gradient(12% 9% at 78% 82%,#B42318 0%,#B42318 60%,rgba(0,0,0,0) 61%),linear-gradient(160deg,#F5F1E6 0%,#EDE7D8 60%,#D9D2C0 100%)',
        { person: .75, pet: .8, landscape: .95, object: .9 },
        [
          ['curve', { contrast: .18, brightness: .05, saturation: -.92, gamma: 1.04 }],
          ['duotone', { dark: [38, 36, 34], light: [245, 241, 230], amount: .88 }],
          ['soften', { radius: 2, mix: .3 }],
          ['paper', { amount: .6 }],
          ['fade', { lift: .09 }],
        ]),
      style('press_one_line_01', 'One Line', 'One continuous line, one quiet accent.', ['portrait', 'object'], false,
        'linear-gradient(150deg,#FBFAF6 0%,#F4F2EA 70%,#ECe8DC 100%)',
        { person: .85, pet: .8, landscape: .5, object: .9 },
        [
          ['curve', { contrast: .5, brightness: .12, saturation: -1, gamma: 1.15 }],
          ['posterize', { levels: 3, amount: .8 }],
          ['duotone', { dark: [35, 35, 35], light: [251, 250, 246], amount: .92 }],
          ['fade', { lift: .06 }],
        ]),
      style('press_studio_hour_01', 'Studio Hour', 'Soft-box light, your best hour.', ['portrait'], true,
        'radial-gradient(90% 70% at 50% 30%,rgba(238,234,226,.9) 0%,rgba(0,0,0,0) 60%),linear-gradient(170deg,#D9D5CC 0%,#A8A49B 55%,#6E6B66 100%)',
        { person: .98, pet: .6, landscape: .1, object: .3 },
        [
          ['soften', { radius: 1, mix: .3 }],
          ['curve', { contrast: .08, brightness: .05, saturation: -.08, gamma: .97 }],
          ['vignette', { amount: .22 }],
          ['bloom', { radius: 5, mix: .18 }],
        ]),
    ],
  },
];

// Deterministic, incremental seed — inserts any exhibitions/styles that are
// missing (new catalog entries appear on restart; existing rows are untouched,
// published StyleVersions stay immutable).
export function seedCatalog({ db, uuid, now, q1, run }) {
  const t = now();
  THEMES.forEach((theme, ti) => {
    let exh = q1('SELECT id FROM exhibitions WHERE slug = ?', theme.slug);
    if (!exh) {
      run('INSERT INTO exhibitions (id, slug, title, curatorial_note, edition, editorial_rank, status, created_at) VALUES (?,?,?,?,?,?,?,?)',
        uuid(), theme.slug, theme.title, theme.note, theme.edition, ti, 'published', t);
      exh = q1('SELECT id FROM exhibitions WHERE slug = ?', theme.slug);
    }
    const exhId = exh.id;
    theme.styles.forEach((s, si) => {
      const existing = q1('SELECT id FROM styles WHERE internal_key = ?', s.internalKey);
      const styleId = existing ? existing.id : uuid();
      const versionId = uuid();
      if (!existing) {
        run(`INSERT INTO styles (id, internal_key, slug, status, theme, premium, public_name, short_caption, suitability_tags, created_at)
             VALUES (?,?,?,?,?,?,?,?,?,?)`,
          styleId, s.internalKey, s.internalKey.replaceAll('_', '-'), 'published', theme.slug,
          s.premium ? 1 : 0, s.name, s.caption, JSON.stringify(s.tags), t);
      }
      const spec = {
        schemaVersion: 1,
        identity: { internalKey: s.internalKey, publicName: s.name, theme: theme.slug, tags: s.tags, premium: s.premium },
        intent: { summary: s.caption },
        compatibility: { subjects: s.compatibility, minShortEdge: 320, maxPeople: 6 },
        controls: {
          strength: { default: 'balanced', allowed: ['soft', 'balanced', 'bold'] },
          fidelity: { default: 'high', allowed: ['high', 'natural'] },
          composition: { default: 'keep', allowed: ['keep', 'reframe'] },
        },
        pipeline: s.pipeline,
        promptAssembly: {
          baseDirection: BASE_DIRECTIONS[s.internalKey],
          subjectRules: PROMPT_COMMON.subjectRules,
          controlFragments: { strength: PROMPT_COMMON.strength, fidelity: PROMPT_COMMON.fidelity, composition: PROMPT_COMMON.composition },
          negativeConstraints: STYLE_NEGATIVES[s.internalKey] || PROMPT_COMMON.negativeConstraints,
          // Designed styles: an LLM compiles a per-photo prompt (metaphor,
          // layout recipe, content-derived annotations) before generation.
          compiler: {
            press_zine_poster_01: 'zine',
            press_cover_story_01: 'editorial',
            press_reportage_wash_01: 'reportage',
          }[s.internalKey],
        },
        coverArt: s.coverArt,
        output: { defaultAspectRatio: '4:5', qualityTier: 'standard' },
        provenance: s.internalKey.startsWith('press_')
          ? {
            createdBy: 'internal', sourceClass: 'community_adapted_mit',
            notes: {
              press_zine_poster_01: 'Adapted from LiamGvchi/gc-minimal-zine-poster (MIT); reworded for MuseFrame.',
              press_cover_story_01: 'Adapted from dacnay816y62-hub/fantasy-qiqiguaiguai-skill T1 Portrait Editorial (MIT); reworded for MuseFrame.',
              press_reportage_wash_01: 'Adapted from serenashenn3-art/watercolor-sketch-style (MIT); anchor reworded for MuseFrame.',
              press_ink_seal_01: 'Adapted from sammyteng/illustration-studio oriental-ink-guofeng (MIT); reworded for MuseFrame.',
              press_one_line_01: 'Adapted from sammyteng/illustration-studio editorial-line (MIT); reworded for MuseFrame.',
              press_studio_hour_01: 'Adapted from ZoeZYZY/go-photo-studio-skill studio-classic preset + identity pipeline (MIT); reworded for MuseFrame.',
            }[s.internalKey],
          }
          : { createdBy: 'internal', sourceClass: 'original_clean_room' },
      };
      // Published versions are immutable (spec §11.4). If the catalog definition
      // changed, publish the next version; old projects keep referencing theirs.
      // (Production would gate this behind the Admin release flow.)
      const latest = q1('SELECT version, spec FROM style_versions WHERE style_id = ? AND status = ? ORDER BY version DESC LIMIT 1', styleId, 'published');
      const specJson = JSON.stringify(spec);
      if (!latest) {
        run(`INSERT INTO style_versions (id, style_id, version, status, spec, published_at, created_at)
             VALUES (?,?,?,?,?,?,?)`, versionId, styleId, 1, 'published', specJson, t, t);
      } else if (latest.spec !== specJson) {
        run(`INSERT INTO style_versions (id, style_id, version, status, spec, published_at, created_at)
             VALUES (?,?,?,?,?,?,?)`, versionId, styleId, latest.version + 1, 'published', specJson, t, t);
      }
      if (!existing) run('INSERT INTO exhibition_styles (exhibition_id, style_id, position) VALUES (?,?,?)', exhId, styleId, si);
    });
  });
}

export const PRODUCTS = [
  { internalKey: 'mini_pack', productType: 'pack', displayName: 'Mini Pack', grantedUnits: 8, priceMinor: 399, period: null, featureFlags: {} },
  { internalKey: 'creator_monthly', productType: 'subscription', displayName: 'Creator Monthly', grantedUnits: 40, priceMinor: 999, period: 'month', featureFlags: { premiumStyles: true, priorityQueue: true, highResolution: true } },
  { internalKey: 'creator_annual', productType: 'subscription', displayName: 'Creator Annual', grantedUnits: 480, priceMinor: 6999, period: 'year', featureFlags: { premiumStyles: true, priorityQueue: true, highResolution: true } },
];

export function seedProducts({ q1, run, uuid }) {
  for (const p of PRODUCTS) {
    if (q1('SELECT id FROM products WHERE internal_key = ?', p.internalKey)) continue;
    run(`INSERT INTO products (id, internal_key, product_type, display_name, granted_units, price_minor, currency, period, feature_flags, active)
         VALUES (?,?,?,?,?,?,?,?,?,1)`,
      uuid(), p.internalKey, p.productType, p.displayName, p.grantedUnits, p.priceMinor, 'USD', p.period, JSON.stringify(p.featureFlags));
  }
}
