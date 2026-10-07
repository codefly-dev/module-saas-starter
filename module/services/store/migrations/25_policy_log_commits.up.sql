-- The local half of the policy log protocol: append → receipt → commit.
--
-- The protocol exists because a transactional audit event plus an append-only
-- table IN THE SAME DATABASE is not an external record of authority. Restoring
-- that database restores revoked authority together with its own history, so the
-- history cannot witness against the state — it was backed up with it. The log
-- is therefore external, and what lives here is only the RECEIPT: proof that an
-- entry reached the log before the narrowing it describes took effect locally.
--
-- Three rows' worth of state make the protocol work, and each exists because a
-- failure between the two writes has to be recoverable:
--
--   1. an APPENDED entry with no commit — the log knows about a narrowing this
--      host has not applied. Every replica must enforce it or refuse to serve
--      until it is reconciled, because the authority of record says the
--      narrowing happened.
--   2. a COMMITTED entry — the narrowing is applied and the receipt is durable
--      beside it.
--   3. the CURSOR — how far this host has reconciled the log, so a restart
--      resumes rather than re-reads from the beginning, and so the gap in (1)
--      is computable without scanning everything.
--
-- The direction of the asymmetry is the whole design: append-ok/commit-fail must
-- fail CLOSED (the narrowing is enforced), while commit-without-append must be
-- impossible (nothing narrows before the log has witnessed it).

CREATE TABLE public.policy_log_commits (
    -- operation_id is the IDEMPOTENCY KEY, and it is the caller's, not
    -- generated here. A retry of the same logical operation — the same
    -- revocation, the same tenancy change — must append once and commit once
    -- however many times it is attempted, and only the caller knows that two
    -- attempts are the same operation.
    operation_id text NOT NULL,

    -- The receipt the log returned for this operation. Its presence is what
    -- distinguishes "the log witnessed this" from "this host decided it".
    receipt text NOT NULL,

    -- The log's own sequence for the appended entry. Monotonic within the log,
    -- and what the cursor below is compared against.
    log_sequence bigint NOT NULL,

    -- What the operation narrowed, kept so a reconciliation can tell whether a
    -- gap has already been closed by other means without re-reading the log
    -- entry's payload.
    subject_kind text NOT NULL,
    subject_id text NOT NULL,

    appended_at timestamp with time zone NOT NULL,
    -- NULL while the append has landed and the local narrowing has not. That is
    -- the state every replica must treat as "enforce or refuse".
    committed_at timestamp with time zone,

    CONSTRAINT policy_log_commits_operation_id_check
        CHECK ((length(operation_id) BETWEEN 1 AND 512)),
    CONSTRAINT policy_log_commits_receipt_check
        CHECK ((length(receipt) BETWEEN 1 AND 1024)),
    CONSTRAINT policy_log_commits_sequence_check
        CHECK ((log_sequence > 0)),
    CONSTRAINT policy_log_commits_subject_kind_check
        CHECK ((subject_kind = ANY (ARRAY[
            'principal'::text, 'binding'::text, 'installation'::text,
            'team_membership'::text, 'scope_grant'::text]))),
    CONSTRAINT policy_log_commits_subject_id_check
        CHECK ((length(subject_id) BETWEEN 1 AND 512))
);

-- ONE row per operation. This is what makes an idempotent retry idempotent: a
-- second attempt collides here rather than appending to the log twice, and a log
-- with two entries for one operation cannot be replayed into a single answer.
ALTER TABLE ONLY public.policy_log_commits
    ADD CONSTRAINT policy_log_commits_pkey PRIMARY KEY (operation_id);

-- The gap query: appended, not committed. Partial, because the uncommitted set
-- is small and is read on every serving check — a full-table index would be
-- read for a predicate that is almost always empty.
CREATE INDEX policy_log_commits_uncommitted
    ON public.policy_log_commits USING btree (log_sequence)
    WHERE (committed_at IS NULL);

CREATE INDEX policy_log_commits_subject
    ON public.policy_log_commits USING btree (subject_kind, subject_id);

-- How far this host has reconciled the external log.
--
-- ONE row, enforced by the primary key on a constant. A cursor that could have
-- two rows would have two answers to "how far have we read", and the safe
-- reading (the lower) would silently re-apply entries while the unsafe one
-- (the higher) would skip them.
CREATE TABLE public.policy_log_cursor (
    id boolean DEFAULT true NOT NULL,
    -- The highest log sequence this host has read AND accounted for. Entries
    -- above it are unknown to this host; entries at or below it are either
    -- committed or present in policy_log_commits as a gap.
    reconciled_sequence bigint DEFAULT 0 NOT NULL,
    -- When the log was last reached. The serving gate reads this: a host that
    -- cannot reach the log does not know whether its authority is current, and
    -- the maximum-security answer to that is to stop serving rather than to
    -- serve what it last believed.
    reached_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,

    CONSTRAINT policy_log_cursor_singleton CHECK ((id = true)),
    CONSTRAINT policy_log_cursor_sequence_check CHECK ((reconciled_sequence >= 0))
);

ALTER TABLE ONLY public.policy_log_cursor
    ADD CONSTRAINT policy_log_cursor_pkey PRIMARY KEY (id);

INSERT INTO public.policy_log_cursor (id, reconciled_sequence) VALUES (true, 0);

-- Control-plane only, like every other platform relation. Both are UPDATEable:
-- the commit row is completed by the commit, and the cursor advances. Neither is
-- deletable — a receipt is the evidence that an append happened, and deleting it
-- would make an unreconciled gap disappear rather than be closed.
GRANT SELECT,INSERT,UPDATE ON TABLE public.policy_log_commits TO app_control_plane;
GRANT SELECT,INSERT,UPDATE ON TABLE public.policy_log_cursor TO app_control_plane;
