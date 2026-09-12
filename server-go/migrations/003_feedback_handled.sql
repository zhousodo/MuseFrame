-- 003_feedback_handled.sql —— 反馈「已处理」标记。由 museframe_owner 执行。
--
-- 背景：user_feedback 里的 comment 列从建库起就在写（POST /v1/candidates/{id}/feedback
-- 接受 comment 字段并落库），但后台的 GET /v1/admin/feedback 从来没 SELECT 它 ——
-- 用户写的差评正文在后台完全不可见，唯一的读法是去数据库浏览器翻表。
-- 同时运营没有任何「这条我看过了 / 已经处理了」的去处：每次打开反馈列表
-- 都要重新从头判断哪几条是新的。
--
-- 这两个新列就是那个去处。刻意**不建新表**：一条反馈最多只有一个「当前处理状态」，
-- 建历史表等于把「谁什么时候改的」这个问题从审计里再实现一遍 ——
-- 而每一次标记/取消标记都已经落一条 admin.feedback_handled 审计。
--
-- 幂等：可重复执行（IF NOT EXISTS）。
--
-- 🔴 列权限：002_grants.sql 的 GRANT ... ON ALL TABLES 是**表级**授权，
--    表级 DML 权限自动覆盖后加的列，所以这里不需要再 GRANT 一次。

BEGIN;

ALTER TABLE user_feedback ADD COLUMN IF NOT EXISTS handled_at   timestamptz;
ALTER TABLE user_feedback ADD COLUMN IF NOT EXISTS handled_note text;

-- 运营默认只看「未处理」那一叠，所以筛选条件是 handled_at IS NULL。
-- 部分索引只收未处理行：已处理的反馈会无限增长，但永远不会出现在这个筛选里。
CREATE INDEX IF NOT EXISTS feedback_unhandled_idx
  ON user_feedback(created_at DESC) WHERE handled_at IS NULL;

COMMIT;
