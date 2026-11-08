-- An installation's consent follows its target's release line. The shared
-- authority core now records on the consent receipt the generation and the
-- offer digest an approval consented to (ConsentReceipt.Generation, .Digest),
-- advances both when a redelivery asks nothing new, holds them while a
-- redelivery asks more (upgrade_available), and keeps the declaration each
-- consent approved per module and generation, from which it decides what a
-- holder holds while the consent is behind the delivered generation.
--
-- The approved declarations: one row per (module, generation) a consent was
-- taken at or advanced to, written by the core; the current generation of a
-- module stays in authority_core_modules.
CREATE TABLE public.authority_core_approved_modules (
    name text NOT NULL,
    generation bigint NOT NULL,
    principal text NOT NULL,
    kind text NOT NULL,
    stand_in boolean DEFAULT false NOT NULL,
    declarations jsonb NOT NULL,
    CONSTRAINT authority_core_approved_modules_pkey PRIMARY KEY (name, generation),
    CONSTRAINT authority_core_approved_modules_kind_check CHECK ((kind = ANY (ARRAY['module'::text, 'solution'::text]))),
    CONSTRAINT authority_core_approved_modules_generation_check CHECK ((generation > 0))
);
-- Kept, never erased: nothing deletes an approved declaration (a generation
-- an active consent names must stay readable), so the control plane holds no
-- DELETE.
GRANT SELECT,INSERT,UPDATE ON TABLE public.authority_core_approved_modules TO app_control_plane;

-- A receipt written before the generation and digest were kept carries
-- neither key, which the core reads as an approval of unknown generation and
-- offers as an upgrade once. The host reviewed every one of these approvals
-- itself and kept what it approved beside the receipt (host_plan.Approval), so
-- the approved generation and digest are filled from there: a consent approved
-- at the generation still delivered reads as current, and one approved at an
-- older generation reads as upgrade_available, as it is.
--
-- One UPDATE over the core's installations; its row locks are bounded so a
-- long transaction on the table fails this migration to be retried instead of
-- queueing every reader behind it.
SET LOCAL lock_timeout = '15s';
UPDATE public.authority_core_installations
   SET consent = consent
       || jsonb_build_object('Generation', (consent->'host_plan'->'Approval'->>'Generation')::bigint)
       || jsonb_build_object('Digest', consent->'host_plan'->'Approval'->>'Digest')
 WHERE consent ? 'host_plan'
   AND NOT (consent ? 'Generation')
   AND consent->'host_plan'->'Approval'->>'Generation' IS NOT NULL
   AND consent->'host_plan'->'Approval'->>'Digest' IS NOT NULL;

-- The declaration approved at a consent's generation is written here only
-- where that generation is the module's current one: the store holds only the
-- current declaration of each module, and it is exactly what was approved
-- only then. A consent approved at an older generation gets no row from this
-- migration. Writing the current declaration under the older generation
-- would let the core decide that consent's holders from a declaration nobody
-- approved for them, a redelivery's widened roles included. Instead accounts,
-- when it configures the core at boot, rebuilds the declaration of that older
-- generation from the delivery inbox (solution_delivery_documents keeps every
-- generation's signed carrier and authority document, append-only) and writes
-- it under that generation; where the inbox holds no document for it, nothing
-- is written, the core refuses issuance through that installation by name
-- (the approved generation is not kept), and the installation reads as
-- upgrade_available until an administrator reviews and installs the
-- delivered release on it.
INSERT INTO public.authority_core_approved_modules (name, generation, principal, kind, stand_in, declarations)
SELECT DISTINCT m.name, m.generation, m.principal, m.kind, m.stand_in, m.declarations
  FROM public.authority_core_installations i
  JOIN public.authority_core_modules m ON m.name = i.target
 WHERE i.status IN ('pending', 'active')
   AND i.consent ? 'Generation'
   AND (i.consent->>'Generation')::bigint = m.generation
ON CONFLICT (name, generation) DO NOTHING;
