-- An installation names the immutable target it was consented to, never the
-- reusable route alias. This is the destructive half migration 20 deferred, and
-- it arrives with its readers.
--
-- The hole it closes, which existed for as long as `solution_identifier` was the
-- join key:
--
--   1. Org A installs the binding behind alias `reports`, and a team is granted
--      access at the installation's authority-root scope node.
--   2. That binding is withdrawn by a tombstone generation.
--   3. A DIFFERENT binding claims the alias `reports`.
--   4. The installation still names the text `reports`.
--   5. The proxy admits A's viewers to the new binding and forwards their
--      bearers to it, and A's team grant exposes it — with no administrator
--      having acted.
--
-- A rename is the same defect from the other side: the installation keeps the
-- old text, so A silently loses access and whoever next claims the old alias
-- inherits A's installation.
--
-- `target_id` is a foreign key to `solution_targets`, one row per continuous
-- period of one binding's presence (migration 20). It is never reused, so
-- inheritance is not expressible: the withdrawn binding's target is CLOSED and
-- the replacement's is a different row.
--
-- The alias is not stored here at all. It lives on the target, where a
-- generation may move it, and the gateway resolves alias -> target through the
-- applied-presence snapshot it already holds. That deliberately keeps
-- `solution_targets` control-plane only: joining it into the request-path
-- installation listing would have required granting request traffic SELECT over
-- every organisation's presence state to answer a question about one.

ALTER TABLE public.installations
    ADD COLUMN target_id uuid,
    -- Why a row is revoked, when it was revoked by something other than an
    -- administrator uninstalling. The cutover below is the first writer; a
    -- tenancy narrowing that withdraws an organisation's installation is the
    -- second. Without it, a revoked row cannot be told from an uninstall.
    ADD COLUMN revoked_reason text;

-- The deterministic disposition for rows that predate the target identity.
--
-- `installations` FORCEs row-level security and its policies admit only the
-- request and control-plane roles, so the owner running this migration matches
-- zero rows through them. RLS is lifted for these two statements and restored
-- immediately after, exactly as migrations 2 and 14 do.
ALTER TABLE public.installations NO FORCE ROW LEVEL SECURITY;

-- An active installation whose named alias is served by exactly one LIVE target
-- is carried over to that target. "Exactly one" is a property of the schema
-- rather than a hope: `solution_targets_live_solution` is a unique index over
-- `solution_id` where the target is open, so this join can match at most one row.
-- The alias alone is NOT enough to carry consent over, and matching on it alone
-- performs exactly the transfer the next statement's comment says this migration
-- exists to prevent.
--
-- A route alias is a REUSABLE property of a target, never its identity. If a
-- binding was withdrawn and a different one later took the same alias, the "one
-- live target serving this alias" at migration time belongs to the SECOND
-- binding — and an installation consented to the first would silently become an
-- installation of the second. Same alias, different solution, consent moved
-- without anyone acting.
--
-- `i.created_at >= t.opened_at` is what closes it: the installation must have
-- been created during the period this target has been open. An installation that
-- predates the target cannot have consented to it, so it falls through to the
-- revocation below rather than being carried across a period boundary.
--
-- This is strictly narrower than the alias match, so it can only ever revoke
-- more — which is the right direction for a disposition that cannot be reviewed
-- case by case.
UPDATE public.installations AS i
   SET target_id = t.id
  FROM public.solution_targets AS t
 WHERE t.closed_generation IS NULL
   AND t.solution_id = i.solution_identifier
   AND i.created_at >= t.opened_at
   AND i.status = 'active';

-- Everything else is REVOKED, with the reason recorded.
--
-- This is the fail-closed disposition and it is deliberately not a choice
-- between two reasonable answers. An active installation whose alias no live
-- target serves is one of exactly three things: a solution that was never
-- declared (so nothing on this host can establish what was consented to), one
-- that was withdrawn (so the consent already ended), or one whose alias now
-- belongs to a different binding (so carrying it over would commit the very
-- transfer this migration exists to prevent). Keeping it active would mean
-- guessing, and the guess that fails is the one that grants.
--
-- A revoked installation is not a deletion: the row, its authority-root scope
-- node and its audit trail all remain, so re-installing is an administrator's
-- act on a record that can be read.
UPDATE public.installations
   SET status         = 'revoked',
       revoked_at     = now(),
       revoked_reason = 'no live solution target served this installation''s route alias at the installation-identity cutover'
 WHERE status = 'active'
   AND target_id IS NULL;

ALTER TABLE public.installations FORCE ROW LEVEL SECURITY;

ALTER TABLE public.installations
    ADD CONSTRAINT installations_target_id_fkey
        FOREIGN KEY (target_id) REFERENCES public.solution_targets(id);

-- Revoked history keeps `target_id` NULL when none could be established, so the
-- column cannot be NOT NULL. The invariant that matters is narrower and is the
-- one a reader depends on: an ACTIVE installation always names a target.
ALTER TABLE public.installations
    ADD CONSTRAINT installations_active_requires_target
        CHECK (((status <> 'active') OR (target_id IS NOT NULL)));

-- A revoked row states why. Two IS NULL tests compared are never NULL, so this
-- cannot pass vacuously on a row that set neither.
ALTER TABLE public.installations
    ADD CONSTRAINT installations_revoked_reason_only_when_revoked
        CHECK (((revoked_reason IS NULL) OR (status = 'revoked')));

-- The free-text identifier goes. Its unique index goes with it: uniqueness is
-- now per target, which is what makes re-installing a withdrawn solution a new
-- row rather than a collision with the installation whose consent ended.
DROP INDEX IF EXISTS public.idx_installations_active_solution;

ALTER TABLE public.installations
    DROP COLUMN solution_identifier;

CREATE UNIQUE INDEX idx_installations_active_target
    ON public.installations USING btree (org_id, target_id)
    WHERE (status = 'active'::text);

CREATE INDEX idx_installations_target
    ON public.installations USING btree (target_id);

-- `target_id` is IMMUTABLE once set. A column with a foreign key can still be
-- repointed by any writer holding UPDATE, and repointing it is precisely the
-- transfer of consent the column exists to make impossible — so the rule is
-- enforced below every writer rather than in each one.
--
-- Clearing it is refused for the same reason: a NULL target on a row that had
-- one would let the next writer set a different one.
CREATE OR REPLACE FUNCTION public.installations_target_is_immutable()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = public, pg_temp
AS $$
BEGIN
    IF OLD.target_id IS NOT NULL AND (NEW.target_id IS NULL OR NEW.target_id <> OLD.target_id) THEN
        RAISE EXCEPTION
            'installation % names solution target %, which is immutable: a replacement solution needs its own installation',
            OLD.id, OLD.target_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER installations_target_immutable
    BEFORE UPDATE ON public.installations
    FOR EACH ROW
    EXECUTE FUNCTION public.installations_target_is_immutable();

-- An ACTIVE installation's target must be LIVE.
--
-- The business layer resolves the target before the install transaction — it has
-- to, because `solution_targets` is control-plane only and the install runs in a
-- tenant transaction — and a resolve-then-insert cannot be race-free on its own:
-- the tombstone that closes the target may be applied in between. So the rule is
-- enforced here, where the insert and the target row are in one transaction and
-- the reconciler's row lock orders them.
--
-- SECURITY DEFINER, deliberately, and this is the narrowest form it can take: the
-- request role holds no grant on `solution_targets`, so an invoker-rights
-- function would read zero rows and the check would pass for every closed
-- target — a denied read printing as an empty table is the failure mode this
-- avoids. The function reads ONE row by primary key and either raises or does
-- not; it returns nothing, writes nothing, and takes no caller-supplied SQL.
CREATE OR REPLACE FUNCTION public.installations_target_must_be_live()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
DECLARE
    closed bigint;
    found boolean;
BEGIN
    IF NEW.status <> 'active' OR NEW.target_id IS NULL THEN
        RETURN NEW;
    END IF;
    -- FOR UPDATE, because reading the target without locking it lets this
    -- insert race a withdrawal: the trigger reads "open", a concurrent
    -- transaction closes the target, and both commit — leaving an ACTIVE
    -- installation of a withdrawn target, which is the state the trigger exists
    -- to make unreachable.
    --
    -- Withdrawal closes a target with an UPDATE, which takes the same row lock,
    -- so the two serialize: whichever reaches the row first wins and the other
    -- sees its committed result. Locking the target rather than the
    -- installation is what makes this work — the two transactions touch
    -- different installation rows and would never contend there.
    SELECT t.closed_generation, TRUE INTO closed, found
      FROM public.solution_targets t
     WHERE t.id = NEW.target_id
       FOR UPDATE;
    IF NOT COALESCE(found, FALSE) THEN
        RAISE EXCEPTION 'solution target % does not exist', NEW.target_id
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    IF closed IS NOT NULL THEN
        RAISE EXCEPTION
            'solution target % was withdrawn at generation %, so it cannot be installed: a replacement presence has its own target',
            NEW.target_id, closed
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

REVOKE ALL ON FUNCTION public.installations_target_must_be_live() FROM PUBLIC;

CREATE TRIGGER installations_target_live_on_insert
    BEFORE INSERT ON public.installations
    FOR EACH ROW
    EXECUTE FUNCTION public.installations_target_must_be_live();

-- Also on UPDATE, so reactivating a revoked installation whose target has since
-- been withdrawn is refused by the same rule rather than by whichever writer
-- happened to remember it.
-- The registry record carries the target its declaration opened.
--
-- Without it, a consumer asked about a route alias has no way to reach the
-- identity that currently serves it: it would have to join `solution_targets`,
-- which request traffic holds no grant on, or fall back to comparing the alias —
-- which is the defect. A record with NO declaration carries no target and is
-- therefore admissible to nobody, which is the fail-closed answer.
ALTER TABLE public.solution_registrations
    ADD COLUMN declared_target_id uuid REFERENCES public.solution_targets(id);

-- A declaration is whole: binding, generation, release and target arrive
-- together or not at all. The existing three-column form let a target be absent
-- from a record that was otherwise declared, which would read as "declared but
-- installable by nobody" — indistinguishable from the fail-closed answer for an
-- undeclared record, and therefore impossible to diagnose.
--
-- Existing declarations are completed, or cleared.
--
-- A record declared before this column existed has a binding, a generation and a
-- release and no target, which the four-column invariant below refuses. The
-- target is recoverable for most of them — a declared record's presence is the
-- LIVE target of the binding that declared it — so those are completed in place.
--
-- A declaration whose binding has no live target cannot be completed: either the
-- presence was withdrawn, or the target rows predate the binding. Such a record
-- has its declaration CLEARED whole, which makes it undeclared — admissible to
-- nobody, and visible as such — rather than carrying three quarters of a
-- declaration past a constraint that exists to say a declaration is whole.
--
-- `solution_registrations` is control-plane only (no tenant column, no RLS), so
-- unlike `installations` above this needs no RLS suspension; the migration owner
-- reaches these rows directly.
UPDATE public.solution_registrations AS r
   SET declared_target_id = t.id
  FROM public.solution_targets AS t
 WHERE r.declared_binding_id IS NOT NULL
   AND r.declared_target_id IS NULL
   AND t.binding_id = r.declared_binding_id
   AND t.closed_generation IS NULL;

UPDATE public.solution_registrations
   SET declared_binding_id = NULL,
       declared_generation = NULL,
       declared_release    = NULL,
       declared_target_id  = NULL
 WHERE declared_binding_id IS NOT NULL
   AND declared_target_id IS NULL;

-- Migration 19's three-column version is REPLACED rather than left beside this
-- one. Two overlapping whole-or-absent checks are satisfiable only by their
-- intersection, so keeping both would make the effective invariant something
-- neither constraint states — the kind of schema a reader has to compute.
ALTER TABLE public.solution_registrations
    DROP CONSTRAINT solution_registrations_declared_whole;

ALTER TABLE public.solution_registrations
    ADD CONSTRAINT solution_registrations_declared_whole
        CHECK ((num_nonnulls(declared_binding_id, declared_generation, declared_release, declared_target_id) = ANY (ARRAY[0, 4])));

CREATE TRIGGER installations_target_live_on_update
    BEFORE UPDATE ON public.installations
    FOR EACH ROW
    WHEN (NEW.status = 'active' AND OLD.status <> 'active')
    EXECUTE FUNCTION public.installations_target_must_be_live();
