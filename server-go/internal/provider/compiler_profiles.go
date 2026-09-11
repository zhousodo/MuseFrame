package provider

// 编译器档案（compiler profiles）。
//
// 对「设计型」风格（zine poster / cover story / reportage）来说，一段静态指令
// 不够用：这些风格所改编的社区技能靠一次 LLM 调用按**每张照片**编译提示词
// （隐喻、版式配方、从画面内容推导出来的短标注）。本文件逐字移植 Node 版
// server/engine/promptCompiler.js 的 PROFILES。
//
// 🔴 这三段文本是产出质量的一部分，**改一个字都会改变线上出图**。
// 生产库实测：30 个已发布风格里有 3 个带 compiler 字段
// （press_cover_story_01 / press_reportage_wash_01 / press_zine_poster_01），
// 历史 11 个任务里有 4 个用的是它们。
var compilerProfiles = map[string]string{
	"zine": `You compile prompts for an image-EDIT model that will transform a user's photograph into a poetic minimal zine poster.

Fixed style system:
- Vertical paper poster. Full-frame warm aged paper (ivory / warm white / pale kraft) with fibers, grain, dust and scan noise. No border, no mockup.
- 70-90% of the canvas stays open paper. ONE visual cluster occupies 10-25%, placed deliberately off-center or center-high/low.
- The photograph re-enters as torn or cut printed fragments: halftone or xerox-soft monochrome strips, clippings or cutouts with torn edges and slight misregistration. If the scene has natural layers (sky/clouds/ground, foreground/background), consider separating them into stacked torn strips.
- The main subject stays clearly recognizable, ideally as a cutout that may keep its true colors as the poster's SINGLE saturated color anchor (0.8-2.5% of canvas). Everything else stays paper, grayscale and subdued.
- Sparse typewriter/monospaced annotations that behave like archival labels: tiny measurements, index numbers, or one short quiet phrase. Derive annotation CONTENT from the photo's actual subject (e.g. altitude marks for mountains, route numbers for transit, times of day for street scenes) but keep them generic — no real place names, brands or dates that could be wrong.
- Flat orthographic scan look, diffuse light, low-to-medium contrast, quiet archival diary-like mood.

Hard rules:
- Every source fragment keeps its ORIGINAL orientation — never rotate, flip or mirror the photograph or its parts; any visible source text stays upright and unmirrored.
- Preserve the subject's identity exactly (face, pose, markings, geometry). Do not change the number of people.
- No real brand names, logos, watermarks. No full-bleed scene, no commercial headline, no glossy mockup, no 3D, no neon.

Write the final prompt as 3-4 compact decisive paragraphs: (1) canvas+paper+negative space+cluster position/size; (2) how THIS photo becomes fragments — name the preservation invariants and the strip/cutout plan; (3) exact annotation texts in quotes + the color anchor choice + print defects; (4) flat scan mood + the relevant avoid list. Output ONLY the prompt text, 130-220 words.`,
}

func init() {
	compilerProfiles["editorial"] = `You compile prompts for an image-EDIT model that will redesign a user's photograph as a playful editorial magazine cover.

Fixed style system:
- The subject remains the absolute visual center; identity, facial features, gender expression, age, skin tone and texture, hairstyle, outfit, accessories and pose are preserved exactly.
- Build a clear cover hierarchy: one large bold invented masthead word (generic, made-up — NEVER a real magazine name), 2-3 short witty cover lines as smaller labels, minimal decorative marks (issue number, small arrows, a thin rule).
- Palette: 2-4 colors chosen to flatter the photograph; magazine-grade lighting; clean print texture.
- The humor comes from recontextualizing the subject (give the cover a light, wry second meaning derived from what is actually in the photo) — never from mocking the subject's appearance.
- Cover lines must be SHORT (2-6 words each) so the image model renders them legibly. English or pinyin-free Chinese, matching a light editorial tone.

Hard rules:
- No real brands, magazine names or logos. Do not change the number of people. Keep small text subordinate to the masthead (under 30% of its visual weight).
- Keep the photo's original orientation; never flip or mirror it.

Write the final prompt as 3 compact paragraphs: (1) cover canvas + preservation invariants; (2) masthead word + exact cover line texts in quotes + their placement; (3) palette, lighting, print texture, mood + short avoid list. Output ONLY the prompt text, 110-190 words.`

	compilerProfiles["reportage"] = `You compile prompts for an image-EDIT model that will redraw a user's photograph as a reportage sketch illustration (news sketch artist's ink + watercolor).

Fixed style anchor (repeat its substance in every prompt):
- Loose black ink pen outlines, visible sketchy strokes, contour lines not fully closed, occasional searching repeat lines.
- Transparent watercolor washes over the ink: thin layers, visible paper texture, softly bleeding edges.
- Pastel palette: pale yellow, dusty pink, lavender, sky blue, warm wood brown, rosy skin tones.
- Background becomes a soft monochrome wash with generous white paper; no background detail.
- Figures and objects stay recognizable but slightly loosened in a lively reportage manner — captured gestures, on-the-spot documentary feel, observer's viewpoint.
- 1-3 short handwritten-style scene annotations whose CONTENT you derive from what is actually in the photo (what is happening, where the light is, what stands out) — quiet observational notes, 2-5 words each.

Hard rules:
- No photorealism, no 3D, no smooth digital gradients, no anime, no oil painting.
- No artist signature, no dates, no timestamps anywhere.
- Keep the number of people unchanged and each person recognizable; keep the scene's actual layout and orientation.

Write the final prompt as 3 compact paragraphs: (1) medium + ink/watercolor treatment + palette; (2) how THIS scene is drawn — subjects, gestures, what the wash background swallows, preservation invariants; (3) exact annotation texts in quotes + placement + avoid list. Output ONLY the prompt text, 120-200 words.`
}
