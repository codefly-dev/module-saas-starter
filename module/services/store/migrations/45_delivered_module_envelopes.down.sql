-- A delivered revision has no writer to restore, and dropping its signature
-- would leave a ceiling nobody can re-verify or attribute; refuse the rollback
-- while any revision was delivered.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.module_envelopes WHERE written_by IS NULL) THEN
  RAISE EXCEPTION 'module_envelopes holds delivered revisions; migration 45 cannot be reverted';
 END IF;
END $$;
ALTER TABLE public.module_envelopes
    DROP CONSTRAINT module_envelopes_writer_check,
    DROP COLUMN delivered_envelope,
    DROP COLUMN delivered_payload,
    DROP COLUMN ownership_domain,
    DROP COLUMN signer,
    ALTER COLUMN written_by SET NOT NULL;
