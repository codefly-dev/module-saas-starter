SET LOCAL lock_timeout = '15s';
ALTER TABLE public.authority_core_installations DROP COLUMN installed_at;
