-- Revoke privileges first; rollback leaves the role itself in place
-- in case Phase-2 migrations also reference it. Manual cleanup can
-- DROP ROLE app_tenant after verifying.
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM app_tenant;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM app_tenant;
REVOKE USAGE ON SCHEMA public FROM app_tenant;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM app_tenant;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM app_tenant;
