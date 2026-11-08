-- An installation's consent follows its target's release line: the shared
-- authority core now records on the consent receipt the generation and the
-- offer digest an approval consented to (ConsentReceipt.Generation, .Digest),
-- advances both when a redelivery asks nothing new, and holds them while a
-- redelivery asks more (upgrade_available). A receipt written before this was
-- kept carries neither key, which the core reads as an unknown approval and
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
