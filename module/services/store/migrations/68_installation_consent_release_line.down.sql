-- Removes the approved generation and digest the up migration filled from the
-- host's own approval, and the approved declarations; the core then reads
-- those receipts as before (an approval of unknown generation). Only a receipt
-- whose generation and digest still equal its host_plan.Approval's is
-- stripped, which is exactly what the up migration (or a fresh install, whose
-- receipt and approval are one) wrote and what applying it again restores. A
-- receipt the core has re-converged since names a later generation than the
-- approval and keeps both keys: they are the core's record of a consent that
-- advanced without a review.
SET LOCAL lock_timeout = '15s';
UPDATE public.authority_core_installations
   SET consent = (consent - 'Generation') - 'Digest'
 WHERE consent ? 'host_plan'
   AND consent->>'Generation' IS NOT DISTINCT FROM consent->'host_plan'->'Approval'->>'Generation'
   AND consent->>'Digest' IS NOT DISTINCT FROM consent->'host_plan'->'Approval'->>'Digest';
DROP TABLE public.authority_core_approved_modules;
