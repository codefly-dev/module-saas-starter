-- Removes the approved generation and digest the up migration filled from the
-- host's own approval, and the approved declarations; the core then reads
-- every earlier receipt as before (an approval of unknown generation). The
-- host's approval itself (host_plan.Approval) is untouched, so applying the up
-- migration again restores the same values.
SET LOCAL lock_timeout = '15s';
UPDATE public.authority_core_installations
   SET consent = (consent - 'Generation') - 'Digest'
 WHERE consent ? 'host_plan';
DROP TABLE public.authority_core_approved_modules;
