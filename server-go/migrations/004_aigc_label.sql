-- 004_aigc_label.sql —— 成品的 AI 生成内容标识状态。由 museframe_owner 执行。
--
-- 背景：《人工智能生成合成内容标识办法》（2025-09-01 施行）要求生成服务
-- 在产出上同时加**显式标识**（画在像素上的角标）与**隐式标识**（文件元数据）。
-- 服务端从本版起对每一张新产出的成品都做这两件事（见 internal/aigc）。
--
-- 这一列记的是「这张资产被标了什么」，是后台资产视图与 App 的 aigcLabeled
-- 字段的唯一来源。不建新表：一条资产只有一个当前标识状态，
-- 建历史表等于把「谁什么时候标的」从 events 审计里再实现一遍。
--
-- 取值：
--   NULL           未标识。**历史成品**（本版之前产出的）与全部源图都在这一档。
--                  🔴 刻意不回溯：回溯要重编码已经交付给用户的图，是一次不可逆的
--                  画质损失 + 一次全量重写，而办法约束的是生成服务此后的产出。
--   'meta'         只有隐式元数据标识（运营把 aigc_label_enabled 关掉了）。
--   'visible+meta' 显式水印 + 隐式元数据，两样都有（默认路径）。
--
-- 幂等：可重复执行（IF NOT EXISTS）。
--
-- 🔴 列权限：002_grants.sql 的 GRANT ... ON ALL TABLES 是**表级**授权，
--    表级 DML 权限自动覆盖后加的列，所以这里不需要再 GRANT 一次。

BEGIN;

ALTER TABLE assets ADD COLUMN IF NOT EXISTS aigc_label text;

-- 部分索引只收**成品**里没有标识的行：运营要查的问题只有一个
-- 「还有哪些成品没标识」，而源图永远是 NULL、数量还是成品的好几倍。
CREATE INDEX IF NOT EXISTS assets_unlabeled_candidate_idx
  ON assets(created_at DESC)
  WHERE kind = 'candidate' AND aigc_label IS NULL;

COMMIT;
