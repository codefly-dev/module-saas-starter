-- Back to the five kinds. Any row carrying `organization` is deleted first:
-- re-adding the narrower constraint would otherwise fail validation, and a down
-- migration that cannot run is not a down migration.
--
-- Deleting a commit row here is safe in the one direction that matters. The row
-- records that an append LANDED and the local narrowing committed; removing it
-- makes this host read the operation as unreconciled, which is the fail-closed
-- side — the serving gate then refuses until an operator closes the gap, rather
-- than a narrowing silently reading as applied.
DELETE FROM public.policy_log_commits WHERE subject_kind = 'organization';

ALTER TABLE ONLY public.policy_log_commits
    DROP CONSTRAINT IF EXISTS policy_log_commits_subject_kind_check;

ALTER TABLE ONLY public.policy_log_commits
    ADD CONSTRAINT policy_log_commits_subject_kind_check
        CHECK ((subject_kind = ANY (ARRAY[
            'principal'::text, 'binding'::text, 'installation'::text,
            'team_membership'::text, 'scope_grant'::text])));
