DROP TABLE IF EXISTS public.client_authorization_codes;

ALTER TABLE public.sessions DROP COLUMN IF EXISTS client_id;
