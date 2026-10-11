-- The policy log's subject-kind set gains `organization`, because archiving an
-- organization is a narrowing whose subject is not a grant hanging off one.
--
-- WHY A NEW MIGRATION RATHER THAN AN EDIT TO 22. The closed set lives in two
-- places — Go's `PolicyLogSubjectKind` and this CHECK — and only the Go half was
-- extended, so every `DeleteOrganization` failed with
-- `policy_log_commits_subject_kind_check` AFTER its append had already landed:
-- the log said a narrowing happened and the local commit refused, which is the
-- unwitnessed-narrowing gap the protocol exists to make visible. Editing 22 in
-- place would not have fixed it on any database that already has 22 applied: the
-- migration runner keys on the VERSION and keeps no content checksum, so an
-- in-place change to an applied migration is silently skipped and the old
-- constraint stays.
--
-- The new set is a strict SUPERSET of the old, so revalidation of existing rows
-- cannot fail: every row already carries one of the five kinds this keeps.
ALTER TABLE ONLY public.policy_log_commits
    DROP CONSTRAINT IF EXISTS policy_log_commits_subject_kind_check;

ALTER TABLE ONLY public.policy_log_commits
    ADD CONSTRAINT policy_log_commits_subject_kind_check
        CHECK ((subject_kind = ANY (ARRAY[
            'principal'::text, 'binding'::text, 'installation'::text,
            'team_membership'::text, 'scope_grant'::text,
            'organization'::text])));
