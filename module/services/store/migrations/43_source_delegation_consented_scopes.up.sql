-- What a person actually consented to, so that a binding CHANGING does not by
-- itself revoke what they delegated.
--
-- Until now the delegation carried only `binding_digest` — the SHA-256 of the
-- binding's audience and delegation scopes at connect time — and any digest
-- change revoked the delegation. The digest covers the whole binding, so the
-- rule fired on every edit to what the module asks for, including a NARROWING,
-- which can never need consent because it is strictly less authority.
--
-- The effect was that a module asking for one action more re-authorized every
-- person who had ever connected a source, and the product's only remedy is a
-- reconnect. One module upgrade meant everybody reconnects.
--
-- Recording the consented audience and scopes lets the check ask the question
-- consent is actually about: is the binding now asking for MORE than this
-- person agreed to? Same or narrower is honoured at the narrower set; wider
-- revokes, as it always did.
--
-- Nullable, and the digest stays: a row written before this column exists has
-- no recorded scopes and keeps exactly the digest rule it was written under.
ALTER TABLE public.source_delegations
    ADD COLUMN consented_audience text,
    ADD COLUMN consented_scopes jsonb;

COMMENT ON COLUMN public.source_delegations.consented_audience IS
    'The audience the person delegated to, recorded at connect time. NULL for rows written before this column, which fall back to binding_digest alone.';
COMMENT ON COLUMN public.source_delegations.consented_scopes IS
    'The delegation scopes the person agreed to, recorded at connect time, so a later narrowing is honoured and only a widening revokes.';
