# 内嵌字体说明

`NotoSansSC-AIGCSubset.otf` 是 **Noto Sans CJK SC Bold** 的一个子集，
只保留：ASCII（U+0020–U+007E）+ 常用中文标点 + GB2312 全部 6763 个汉字，
并丢掉了 `GSUB/GPOS/GDEF/morx/kerx` 与 hinting。1.5 MB，随二进制 `go:embed` 进镜像。

- 上游：https://github.com/notofonts/noto-cjk
- 原始文件：Debian `fonts-noto-cjk` 包的 `/usr/share/fonts/opentype/noto/NotoSansCJK-Bold.ttc`（face 2 = SC Bold）
- 许可：SIL Open Font License 1.1（见同目录 `OFL-1.1.txt`）。OFL 允许子集化、
  随软件捆绑与再分发；本子集**不**使用 "Noto" 作为保留名下的新字体名销售。
- 子集化命令（可复现）：

      pyftsubset /usr/share/fonts/opentype/noto/NotoSansCJK-Bold.ttc \
        --font-number=2 --text-file=<ASCII+标点+GB2312> \
        --output-file=NotoSansSC-AIGCSubset.otf \
        --no-hinting --desubroutinize --name-IDs='*' \
        --drop-tables+=GSUB,GPOS,GDEF,morx,kerx

## 为什么必须内嵌而不是读系统字体

运行镜像是 `gcr.io/distroless/static-debian12`，里面**一个字体文件都没有**。
水印文字默认含中文（「AI 生成」），靠系统字体等于水印在生产上永远画不出来 ——
而那正是《人工智能生成合成内容标识办法》要求的显式标识。

## 为什么是 GB2312 全集而不是几个字

`aigc_label_text` 是后台可改的运营项。字体里没有的字只能画成空白/豆腐块，
那是个**看不见的**故障：后台显示「已保存」，而线上每张图的水印缺了半句话。
GB2312 全集覆盖日常简体中文书面语；注册表在写入时会逐字校验（见
`cfgstore` 的 `aigc_label_text` 校验），含未覆盖字符的文案直接 422 并列出那些字。
