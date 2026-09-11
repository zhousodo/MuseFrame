-- 角色授权。由 museframe_owner 执行（与 001_init.sql 同一次性 job）。
--
-- museframe_owner    迁移 / DDL
-- museframe_app      运行时：只有 DML，**无 DDL 权限** —— 所以应用启动时不建表
-- museframe_readonly 只读：给 /v1/admin/db/query 的只读 SQL 控制台与运维排障用
--
-- 幂等：可重复执行。

BEGIN;

GRANT USAGE ON SCHEMA public TO museframe_app, museframe_readonly;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO museframe_app;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO museframe_readonly;

-- 将来 owner 新建的表自动带上同样的授权。
ALTER DEFAULT PRIVILEGES FOR ROLE museframe_owner IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO museframe_app;
ALTER DEFAULT PRIVILEGES FOR ROLE museframe_owner IN SCHEMA public
  GRANT SELECT ON TABLES TO museframe_readonly;

-- 运行角色不得建表：这条是「schema 只由一次性 job 管」的强制点。
REVOKE CREATE ON SCHEMA public FROM museframe_app, museframe_readonly;

COMMIT;
