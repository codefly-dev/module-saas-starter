DROP TRIGGER IF EXISTS job_messages_append_transition ON public.job_messages;
DROP TRIGGER IF EXISTS job_messages_enforce_state ON public.job_messages;
DROP TRIGGER IF EXISTS job_attempts_enforce_state ON public.job_attempts;

DROP FUNCTION IF EXISTS public.enqueue_job_message(
    TEXT, TEXT, UUID, UUID, TEXT, TEXT, TEXT, TEXT, TEXT, INTEGER,
    BYTEA, TEXT, JSONB, SMALLINT, INTEGER, TIMESTAMPTZ, BYTEA
);
DROP FUNCTION IF EXISTS public.replay_job_message(UUID, TEXT, TIMESTAMPTZ, BYTEA);

DROP TABLE IF EXISTS public.job_state_transitions;
DROP TABLE IF EXISTS public.job_attempts;
DROP TABLE IF EXISTS public.job_messages;

DROP FUNCTION IF EXISTS public.append_job_state_transition();
DROP FUNCTION IF EXISTS public.enforce_job_message_state();
DROP FUNCTION IF EXISTS public.enforce_job_attempt_state();
DROP FUNCTION IF EXISTS public.job_state_transition_is_valid(TEXT, TEXT);

DO $$
DECLARE
    member_name TEXT;
BEGIN
    FOR member_name IN
        SELECT member.rolname
        FROM pg_auth_members membership
        JOIN pg_roles granted ON granted.oid = membership.roleid
        JOIN pg_roles member ON member.oid = membership.member
        WHERE granted.rolname = 'app_job_worker'
    LOOP
        EXECUTE format('REVOKE app_job_worker FROM %I', member_name);
    END LOOP;
END $$;

REVOKE USAGE, CREATE ON SCHEMA public FROM app_job_worker;
DROP ROLE IF EXISTS app_job_worker;
