SET LOCAL lock_timeout = '15s';
REVOKE SELECT ON TABLE public.schema_migrations FROM app_control_plane;
