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
GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE public.authority_core_approved_modules TO app_control_plane;

-- A receipt written before the generation and digest were kept carries
-- neither key, which the core reads as an approval of unknown generation and
-- offers as an upgrade once. The host reviewed every one of these approvals
-- itself and kept what it approved beside the receipt (host_plan.Approval), so
-- the approved generation and digest are filled from there, and nothing an
-- organization already approved flips to "update available" on the first
-- delivery after this ledger.
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

-- Every generation a live consent now names gets its approved declaration.
-- The store holds only the current declaration of each module; where the
-- approved generation is the current one, that is exactly what was approved.
-- Where an installation approved an older generation, the current declaration
-- is written under the approved generation too, because the one approved is
-- not kept anywhere: nothing is invented, and the difference between the two
-- surfaces as upgrade_available on the next delivery of that module, which
-- compares the consent's digest with the approved generation's.
INSERT INTO public.authority_core_approved_modules (name, generation, principal, kind, stand_in, declarations)
SELECT DISTINCT m.name, (i.consent->>'Generation')::bigint, m.principal, m.kind, m.stand_in, m.declarations
  FROM public.authority_core_installations i
  JOIN public.authority_core_modules m ON m.name = i.target
 WHERE i.status IN ('pending', 'active')
   AND i.consent ? 'Generation'
   AND (i.consent->>'Generation')::bigint > 0
ON CONFLICT (name, generation) DO NOTHING;
