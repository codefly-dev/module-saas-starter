-- A delegation lifetime is authority state and must survive every read.
-- NULL is the explicit until-revoked lifetime; expiry is checked at use.
ALTER TABLE public.authority_core_delegations ADD COLUMN expires_at timestamptz;
