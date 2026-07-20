DO $$
DECLARE
    member_name TEXT;
BEGIN
    FOR member_name IN
        SELECT member.rolname
        FROM pg_auth_members membership
        JOIN pg_roles granted ON granted.oid = membership.roleid
        JOIN pg_roles member ON member.oid = membership.member
        WHERE granted.rolname = 'app_control_plane'
    LOOP
        EXECUTE format('REVOKE app_control_plane FROM %I', member_name);
    END LOOP;
END $$;

REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM app_control_plane;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM app_control_plane;
REVOKE USAGE, CREATE ON SCHEMA public FROM app_control_plane;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE ALL ON TABLES FROM app_control_plane;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE ALL ON SEQUENCES FROM app_control_plane;

DROP ROLE app_control_plane;
