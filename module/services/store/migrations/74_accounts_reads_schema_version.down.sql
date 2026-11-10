SET LOCAL lock_timeout = '15s';
DO $revoke$ BEGIN
 IF to_regclass('public.schema_migrations') IS NOT NULL THEN
  REVOKE SELECT ON TABLE public.schema_migrations FROM app_control_plane;
 END IF;
END $revoke$;
