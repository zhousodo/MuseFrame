// MuseFrame UI strings — English is the key, Chinese is the translation.
// t('Some English text', {n: 3}) → localized string with {n} filled in.
// Prefixed keys ('tag.portrait', 'opt.soft', 'status.ready', 'period.month')
// name enumerations coming from the server; their English form is in EN below.
//
// Language choice: ?lang=zh|en → saved choice (localStorage mf.lang) →
// browser language (zh-* → zh) → en.

const KEY = 'mf.lang';
let lang = 'en';

const EN = {
  'tag.portrait': 'Portrait', 'tag.landscape': 'Landscape', 'tag.object': 'Object', 'tag.pet': 'Pet',
  'tag.person': 'Person', 'tag.people': 'People', 'tag.scene': 'Scene', 'tag.unknown': 'Photo',
  'opt.soft': 'Soft', 'opt.balanced': 'Balanced', 'opt.bold': 'Bold',
  'opt.high': 'High', 'opt.natural': 'Natural',
  'opt.keep': 'Keep', 'opt.reframe': 'Reframe', 'opt.original': 'Original',
  'status.draft': 'Draft', 'status.generating': 'In progress', 'status.ready': 'New', 'status.saved': 'Saved',
  'period.month': 'month', 'period.year': 'year', 'period.once': 'one-time',
};

const ZH = {
  // enumerations
  'tag.portrait': '人像', 'tag.landscape': '风景', 'tag.object': '物件', 'tag.pet': '宠物',
  'tag.person': '人物', 'tag.people': '多人', 'tag.scene': '场景', 'tag.unknown': '照片',
  'opt.soft': '轻', 'opt.balanced': '适中', 'opt.bold': '强',
  'opt.high': '高', 'opt.natural': '自然',
  'opt.keep': '保持', 'opt.reframe': '重新构图', 'opt.original': '原比例',
  'status.draft': '草稿', 'status.generating': '生成中', 'status.ready': '新作品', 'status.saved': '已保存',
  'period.month': '月', 'period.year': '年', 'period.once': '一次性',

  // quota badge
  'Sign up · {n} free': '注册领 {n} 张',
  '{n} left': '剩 {n} 张',
  'No artworks left': '额度已用完',

  // nav
  'Back': '返回',
  'Discover': '发现',
  'Create': '创作',
  'Projects': '作品',
  'Profile': '我的',
  'Sign in / Register': '登录 / 注册',
  'Best with — ': '适合 — ',
  'Works best with — ': '最适合 — ',

  // onboarding
  'Your photos, newly seen.': '你的照片，换一种看法。',
  'One photo in, one artwork out — identity kept, direction clear.': '一张照片进，一件作品出——保留本人面貌，方向清晰。',
  'Choose a direction, not a prompt.': '选一个方向，而不是写提示词。',
  'Curated exhibitions and style cards replace prompt engineering.': '策展式的主题展和风格卡片，代替提示词工程。',
  'Private by default.': '默认私密。',
  'Your photos are used only to create and improve your requested result. Location data is removed.': '照片只用于生成你请求的作品；上传前已去除位置信息。',
  'Curated image making': '策展式影像制作',
  'Continue': '继续',
  'Choose a photo': '选择照片',
  'Explore first': '先逛逛',

  // discover
  'Can’t reach the gallery': '暂时连不上画廊',
  'Check your connection and try again. Your drafts and works are safe.': '请检查网络后重试。你的草稿和作品都还在。',
  'Try again': '重试',
  'AUG 2026': '2026 年 8 月',
  'EXHIBITION · {n} STYLES': '主题展 · {n} 种风格',
  'View exhibition': '进入主题展',
  '{n} free artworks when you register': '注册即送 {n} 张免费作品',
  'Email only — no password, no card.': '只需邮箱——不设密码，不绑卡。',
  'Register': '注册',
  'For your photo': '为你的照片',
  'Directions matched to your shot.': '根据你的照片匹配合适的方向。',
  'Find a direction': '找方向',
  'View all': '查看全部',
  'About our styles': '关于风格',
  'Privacy': '隐私',
  'Contact us': '联系我们',
  'Exhibition': '主题展',

  // import
  'Photo library': '相册',
  'Drop a photo here, or browse': '把照片拖到这里，或点击选择',
  'Choose from your photos': '从相册选择',
  'JPEG, PNG or HEIC · up to 20 MB': 'JPEG、PNG 或 HEIC · 最大 20 MB',
  'Browse': '选择文件',
  'or': '或',
  'Take a photo': '拍一张',
  'Location data is removed. Your photo is used only to create the result you request, then follows your retention setting.': '位置信息会被移除。照片只用于生成你请求的作品，之后按你的保留设置处理。',
  'Images up to 20 MB are supported': '图片最大支持 20 MB',
  'This photo is too small to style well': '这张照片太小，效果会不好',
  'Low resolution — quality may be reduced': '分辨率较低——效果可能打折',
  'Could not read that image — try another': '读不出这张图片——换一张试试',
  'Connection problem — check your network and try again': '网络出了问题——请检查后重试',

  // styles
  '{n} person': '{n} 人',
  'reading…': '识别中…',
  'Choose a direction': '选一个方向',
  'For this photo ': '这张照片 ',
  'Change': '更换',
  'All': '全部',
  'Recommended': '推荐',
  'Selected': '已选',
  'Details': '详情',
  'Preview settings': '预览设置',

  // configure
  'YOUR PHOTO': '你的照片',
  'DIRECTION': '方向',
  'Premium direction — opened on request. You can preview settings freely.': 'Premium 风格——目前需联系我们开通。你可以随意预览设置。',
  'Style strength': '风格强度',
  'Subject fidelity': '主体保真',
  'Composition': '构图',
  'Output ratio': '输出比例',
  'Original': '原比例',
  'Register with your email to receive {n} free artworks — the first one is this photo.': '用邮箱注册即可领取 {n} 张免费作品——第一张就从这张照片开始。',
  'Generating is paused': '生成暂停中',
  'Register to get {n} free artworks': '注册领取 {n} 张免费作品',
  'Generate': '开始生成',
  'Generating is paused — nothing will be charged': '生成暂停中——不会扣除额度',
  'Estimate — 1 artwork · {a}–{b} min': '预计 — 1 张作品 · {a}–{b} 分钟',
  'Estimate — 1 artwork · 20–90 s': '预计 — 1 张作品 · 20–90 秒',
  'Generating is paused right now — nothing used': '生成暂时暂停——未扣除额度',
  'Could not start — try again': '没能开始——请重试',
  'Your artwork is ready — see Projects': '作品已完成——去「作品」查看',
  'This request can’t be created — nothing used': '这个请求无法生成——未扣除额度',
  'Something went wrong — nothing used': '出了点问题——未扣除额度',

  // progress
  'Preparing your photo': '准备照片',
  'Building the direction': '构建方向',
  'Making the image': '生成图像',
  'Checking the result': '检查结果',
  'Step {a} of {b}': '第 {a} / {b} 步',
  'Still working — this one is taking longer than usual.': '还在生成——这一张比平时慢一些。',
  'You can leave — creation continues in the background.': '可以离开——生成会在后台继续。',
  'Back to Discover': '回到发现',

  // result
  'Result': '作品',
  'Original photo': '原图',
  'Styled result': '生成结果',
  'Before': '原图',
  'After': '作品',
  'Hold to see original': '按住查看原图',
  'Compare': '对比',
  'STANDARD': '标准',
  'PRIVATE': '私密',
  'Saved ✓': '已保存 ✓',
  'Save': '保存',
  'Share': '分享',
  'Refine': '调整',
  'Thanks — noted for this direction.': '谢谢——已记录到这个方向。',
  'Face changed': '脸变了',
  'Wrong style': '风格不对',
  'Bad details': '细节有问题',
  'Too strong': '太强了',
  'How did it come out?': '效果怎么样？',
  'Love it': '喜欢',
  'Not quite': '不太对',
  'Saved': '已保存',
  'Could not save — try again': '保存失败——请重试',
  'Sharing not available here — use Save': '这里不支持分享——请用保存',

  // projects
  'In progress': '生成中',
  'Nothing here yet': '这里还空着',
  'Works you create will appear in this room.': '你创作的作品会出现在这里。',
  'Project': '作品',
  'Untitled': '未命名',
  'This project has no work yet': '这个项目还没有作品',
  'That attempt failed — try a new one': '那次生成失败了——再试一次',
  'Could not open project': '打不开这个项目',

  // profile
  'Free account': '免费账户',
  'Manage subscription': '管理订阅',
  'Restore purchases': '恢复购买',
  'No active plan found': '没有找到有效订阅',
  'Purchases restored': '已恢复购买',
  'Purchase history': '购买记录',
  'Privacy & data': '隐私与数据',
  'Language': '语言',
  'Sign out': '退出登录',
  'Delete account': '删除账号',
  'Creator': '创作者',
  'Guest': '游客',
  'Signed in — works and credits sync across devices': '已登录——作品与额度跨设备同步',
  'Works are kept on this device only': '作品目前只保存在本设备',
  'Sign in / Register — {n} free artworks': '登录 / 注册——送 {n} 张免费作品',
  '{n} artworks are included with your account. Need more? Email us and we top you up by hand.': '你的账号含 {n} 张免费作品。需要更多？给我们发邮件，我们会人工为你加额度。',
  'Register with your email to receive {n} free artworks.': '用邮箱注册即可领取 {n} 张免费作品。',
  'All directions unlocked · priority creation': '全部风格已解锁 · 优先生成',
  'Get more artworks': '获取更多额度',

  // auth
  'MuseFrame account': 'MuseFrame 账号',
  'Register with your email': '用邮箱注册',
  '{n} free artworks on sign-up. No password — we email you a 6-digit code. Signing in on a new device uses the same steps.': '注册即送 {n} 张免费作品。不设密码——我们把 6 位验证码发到你的邮箱。换设备登录也是同样的步骤。',
  'Email sign-in is not enabled on this server yet. Please contact {email}.': '邮箱登录暂未在此服务器开通，请联系 {email}。',
  'Continue with Google': '用 Google 继续',
  'Continue with Apple': '用 Apple 继续',
  'Your address is used for sign-in codes and, if you ask for more artworks, to reply to you. Nothing else.': '邮箱只用于发送登录验证码，以及在你申请更多额度时回复你。不用于其他用途。',
  'Welcome, {name}': '欢迎，{name}',
  'Sign-in failed — please try again': '登录失败，请重试',
  'Signed out': '已退出登录',
  'Please enter a valid email address': '请输入有效的邮箱地址',
  'Code sent — check your inbox (and spam)': '验证码已发送——请查看收件箱（和垃圾邮件）',
  'Email sign-in is not enabled yet': '邮箱登录暂未开通',
  'Too many codes requested — wait a few minutes': '验证码请求太频繁——请几分钟后再试',
  'Could not send the code — try again later': '验证码发送失败——请稍后重试',
  'Welcome — {n} free artworks are on your account': '欢迎——{n} 张免费作品已到账',
  'Signed in': '登录成功',
  'That code is not right': '验证码不正确',
  'Your email address': '输入邮箱地址',
  'Sending…': '发送中…',
  'Send me a code': '获取验证码',
  'We sent a code to {email}': '验证码已发送至 {email}',
  '6-digit code': '6 位验证码',
  'Checking…': '验证中…',
  'Use another email': '换个邮箱',
  'Resend code': '重新发送',

  // paywall / contact
  'MuseFrame — Premium directions for {email}': 'MuseFrame — 开通 Premium 风格（{email}）',
  'my account': '我的账号',
  'MuseFrame — more artworks for {email}': 'MuseFrame — 加购额度（{email}）',
  'Hello MuseFrame,\n\nI would like Premium directions enabled on my account ({email}).\n\nThanks!': 'MuseFrame 你好：\n\n我想为我的账号（{email}）开通 Premium 风格。\n\n谢谢！',
  'Hello MuseFrame,\n\nI have used my free artworks and would like more on my account ({email}).\nHow many I would like: \n\nThanks!': 'MuseFrame 你好：\n\n我的免费作品已用完，想为账号（{email}）增加额度。\n想要的张数：\n\n谢谢！',
  'Address copied': '邮箱已复制',
  'Sign up with your email — no password, no card. {n} complete artworks are on us. If you already have an account, the same steps sign you in.': '用邮箱注册——不设密码，不绑卡。{n} 张完整作品由我们请客。已有账号的话，同样的步骤即可登录。',
  'Premium direction': 'Premium 风格',
  'Premium directions are opened on request for now. Email us and we will enable them on your account.': 'Premium 风格目前按需开通。给我们发邮件，我们会为你的账号开启。',
  'Your artworks': '你的额度',
  'You have {n} left. When they run out, email us and we will top up your account by hand.': '你还剩 {n} 张。用完之后给我们发邮件，我们会人工为你加额度。',
  'Your free artworks are used up': '免费作品已用完',
  'You have used the {n} free artworks that come with your account. To keep creating, email us — we top up accounts by hand, usually within a day.': '账号附带的 {n} 张免费作品已经用完。想继续创作，请给我们发邮件——我们会人工加额度，通常一天内处理。',
  'Email us': '联系邮箱',
  'Mention the address you registered with: {email}': '来信请注明你的注册邮箱：{email}',
  'Write to us': '写邮件',
  'Copy address': '复制邮箱',
  'Close': '关闭',
  'Register with email': '用邮箱注册',
  'Questions? ': '有疑问？',
  'Failed or rejected generations never use an artwork.': '失败或被拦截的生成不会扣除额度。',
  'Store purchases are only available in the app': '商店购买仅在手机 App 内可用',
  'Pack added — {n} artworks': '已到账——{n} 张作品',
  'Welcome to Creator': '欢迎加入 Creator',
  'Store purchases are not open yet': '商店购买尚未开通，敬请期待',
  'Purchase could not be verified — contact us if you were charged': '购买校验未通过，如已扣款请联系我们',
  'Purchase failed — you were not charged': '购买失败——未扣款',
  'Or buy in the store': '或在商店购买',
  '{n} artworks / {period} · all directions': '每{period} {n} 张 · 全部风格',
  'Continue — {price} / {period}': '继续 — {price} / {period}',
  'Not ready? {name} — {n} artworks for {price}': '还没准备好？{name} — {price} 得 {n} 张',
  'No previous purchases found': '没有找到之前的购买',

  // catalogue (2026-09 plan B)
  'MuseFrame — buy {name} ({price}) for {email}': 'MuseFrame — 购买{name}（{price}），账号 {email}',
  'Hello MuseFrame,\n\nI would like to buy {name} ({n} artworks, {price}) for my account ({email}).\nPlease tell me how to pay.\n\nThanks!': 'MuseFrame 你好：\n\n我想为账号（{email}）购买{name}（{n} 张，{price}）。\n请告诉我付款方式。\n\n谢谢！',
  'Premium directions come with Creator. Pick the plan below and email us — we enable it on your account, usually within a day.': 'Premium 风格包含在 Creator 里。选下面的套餐给我们发邮件，我们为你的账号开通，通常一天内。',
  'You have {n} left. Packs below add more; email us to buy and we credit your account, usually within a day.': '你还剩 {n} 张。下面的点数包可以加更多；发邮件购买，我们通常一天内为你的账号充值。',
  'You have used the {n} free artworks that come with your account. Pick a pack below and email us — we credit your account by hand, usually within a day.': '账号附带的 {n} 张免费作品已经用完。选一个点数包给我们发邮件，我们人工为你充值，通常一天内到账。',
  'PREMIUM + HIGH-RES': 'PREMIUM + 高清',
  '{n} artworks every {period} · all directions · high tier': '每{period} {n} 张 · 全部风格 · 高清档',
  '{n} artworks · {each} each · never expire': '{n} 张 · 每张 {each} · 不过期',
  'Email to buy': '发邮件购买',
  '{n} artworks are included with your account. Packs start at {price}; email us to buy.': '你的账号含 {n} 张免费作品。点数包 {price} 起，发邮件即可购买。',
  'See packs & prices': '查看套餐与价格',
  'Pricing': '定价',
  'Android app': 'Android 版',
  'Photos are re-encoded on your device before upload; failed generations never use an artwork.': '照片在上传前于你的设备上重新编码；失败的生成不扣额度。',
  // sheets
  'An original MuseFrame direction. Identity, pose and key objects are preserved; light, color and texture follow the direction. May change: background detail, fine texture.': 'MuseFrame 原创风格。保留人物身份、姿态和关键物体；光线、色彩和质感跟随风格走。可能变化的是背景细节与细微纹理。',
  'Premium direction — opened on request.': 'Premium 风格——目前需联系我们开通。',
  'Use this direction': '用这个方向',
  'Your photos are used only to create the results you request. Uploads are re-encoded on your device, which removes location and camera metadata. Source photos are kept for 30 days by default; results stay until you delete them. Deleting a project removes its images from storage.': '照片只用于生成你请求的作品。上传前在你的设备上重新编码，去除位置与相机元数据。原图默认保留 30 天；作品保留到你删除为止。删除项目会同时删除其图片。',
  'All directions are original MuseFrame StyleSpecs, organized in curated exhibitions. Each is a versioned, tested product asset built from public-domain visual principles — no third-party prompt packs, no living artists’ names. Published versions are immutable: your old works always re-render the version they used.': '全部风格都是 MuseFrame 原创的风格规格，按策展主题展组织。每一种都是有版本、经测试的产品资产，基于公有领域的视觉原理——不用第三方提示词包，不用在世艺术家的名字。已发布版本不可变：旧作品永远按当时的版本重现。',
  'To delete your account and its images, email us from the address you registered with. Sign-in is revoked, images enter a purge queue, and legally required payment records are separated and de-identified. To only clear this device, sign out.': '要删除账号及其图片，请用注册邮箱给我们发邮件。登录会被撤销，图片进入清除队列，法律要求保留的支付记录会被分离并去标识化。如只想清除本设备，请退出登录。',
  'No purchases yet. Artworks added by our team after you email us are shown in your balance, not here.': '暂无购买记录。你来信后由我们人工加的额度会显示在余额里，不在这里。',
  'MuseFrame — delete my account ({email})': 'MuseFrame — 删除我的账号（{email}）',
  'Email us to delete': '发邮件申请删除',
  'Reading the image': '正在读取图片',
  'Subject, light and composition — a moment.': '主体、光线与构图——稍等片刻。',
};

function interpolate(s, vars) {
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

export function t(key, vars) {
  let s;
  if (lang === 'zh' && key in ZH) s = ZH[key];
  else if (key in EN) s = EN[key];
  else if (key.includes('.') && /^(tag|opt|status|period)\./.test(key)) {
    const raw = key.slice(key.indexOf('.') + 1);
    s = raw.charAt(0).toUpperCase() + raw.slice(1);
  } else s = key;
  return interpolate(s, vars);
}

export function getLang() { return lang; }

export function setLang(l) {
  lang = l === 'zh' ? 'zh' : 'en';
  try { localStorage.setItem(KEY, lang); } catch { /* private mode */ }
}

/** Pick the language once at boot: URL → saved → browser → en. */
export function initLang(fromUrl) {
  if (fromUrl === 'zh' || fromUrl === 'en') { setLang(fromUrl); return lang; }
  let saved = null;
  try { saved = localStorage.getItem(KEY); } catch { /* ignore */ }
  if (saved === 'zh' || saved === 'en') { lang = saved; return lang; }
  const nav = (navigator.languages && navigator.languages[0]) || navigator.language || '';
  lang = /^zh\b/i.test(nav) ? 'zh' : 'en';
  return lang;
}
