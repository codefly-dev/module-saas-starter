-- The policy log's local half, as migrations 25, 27 and 32 left it: the
-- receipt of each narrowing appended to the external log, and the singleton
-- cursor of how far this host reconciled it. Control-plane only, updateable,
-- never deletable. The rows the up migration dropped are not recoverable.

CREATE TABLE public.policy_log_commits (
    operation_id text NOT NULL,
    receipt text NOT NULL,
    log_sequence bigint NOT NULL,
    subject_kind text NOT NULL,
    subject_id text NOT NULL,
    appended_at timestamp with time zone NOT NULL,
    committed_at timestamp with time zone,

    CONSTRAINT policy_log_commits_operation_id_check
        CHECK ((length(operation_id) BETWEEN 1 AND 512)),
    CONSTRAINT policy_log_commits_receipt_check
        CHECK ((length(receipt) BETWEEN 1 AND 1024)),
    CONSTRAINT policy_log_commits_sequence_check
        CHECK ((log_sequence > 0)),
    CONSTRAINT policy_log_commits_subject_kind_check
        CHECK (subject_kind IN ('principal','binding','installation','team_membership','scope_grant','organization','module_envelope')),
    CONSTRAINT policy_log_commits_subject_id_check
        CHECK ((length(subject_id) BETWEEN 1 AND 512))
);

ALTER TABLE ONLY public.policy_log_commits
    ADD CONSTRAINT policy_log_commits_pkey PRIMARY KEY (operation_id);

CREATE INDEX policy_log_commits_uncommitted
    ON public.policy_log_commits USING btree (log_sequence)
    WHERE (committed_at IS NULL);

CREATE INDEX policy_log_commits_subject
    ON public.policy_log_commits USING btree (subject_kind, subject_id);

CREATE TABLE public.policy_log_cursor (
    id boolean DEFAULT true NOT NULL,
    reconciled_sequence bigint DEFAULT 0 NOT NULL,
    reached_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,

    CONSTRAINT policy_log_cursor_singleton CHECK ((id = true)),
    CONSTRAINT policy_log_cursor_sequence_check CHECK ((reconciled_sequence >= 0))
);

ALTER TABLE ONLY public.policy_log_cursor
    ADD CONSTRAINT policy_log_cursor_pkey PRIMARY KEY (id);

INSERT INTO public.policy_log_cursor (id, reconciled_sequence) VALUES (true, 0);

GRANT SELECT,INSERT,UPDATE ON TABLE public.policy_log_commits TO app_control_plane;
GRANT SELECT,INSERT,UPDATE ON TABLE public.policy_log_cursor TO app_control_plane;
