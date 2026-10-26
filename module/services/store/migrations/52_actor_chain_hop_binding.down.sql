ALTER TABLE public.actor_chain_journal
    DROP CONSTRAINT IF EXISTS actor_chain_journal_operation_whole,
    DROP CONSTRAINT IF EXISTS actor_chain_journal_operation_check,
    DROP COLUMN IF EXISTS operation,
    DROP COLUMN IF EXISTS operation_binding_id;
