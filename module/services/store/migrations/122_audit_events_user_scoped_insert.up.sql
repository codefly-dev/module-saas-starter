-- Let a user-scoped transaction commit its own NULL-org audit row.
--
-- Security mutations that belong to a user rather than a tenant — MFA
-- enrollment and revocation, backup-code rotation, passkey registration,
-- profile and identity changes, consent, GDPR requests — run under
-- WithUserTx / As(Identity{UserID}).Within, which sets app.current_user_id and
-- no org. Their audit rows carry org_id NULL, and until now only the control
-- plane could write those, so the record had to be committed by a second,
-- separate transaction: a crash or an error between the two left the security
-- change in place with nothing recording it.
--
-- The widening is INSERT-only and pinned to the acting user: a transaction may
-- write a NULL-org row only when the row's actor_id is the user the
-- transaction is running as. It cannot write another tenant's rows, cannot
-- attribute a row to anyone else, and the USING clause is untouched — NULL-org
-- rows stay invisible to tenant reads and readable only under the control
-- plane. audit_events remains append-only (no UPDATE/DELETE grant, plus the
-- immutability triggers), so a row committed this way can never be edited away.
ALTER POLICY audit_events_tenant ON audit_events
    USING (
        org_id IS NOT NULL
        AND org_id::text = current_setting('app.current_org_id', true)
    )
    WITH CHECK (
        (
            org_id IS NOT NULL
            AND org_id::text = current_setting('app.current_org_id', true)
        )
        OR (
            org_id IS NULL
            AND actor_id IS NOT NULL
            AND actor_id::text = current_setting('app.current_user_id', true)
        )
    );
