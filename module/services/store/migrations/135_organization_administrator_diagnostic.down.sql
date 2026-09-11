-- The findings are derived data: dropping them loses no record of anything but
-- the scan itself, which the function above reproduces on demand.
DROP FUNCTION IF EXISTS public.record_membership_integrity_findings();
DROP TABLE IF EXISTS membership_integrity_findings;
