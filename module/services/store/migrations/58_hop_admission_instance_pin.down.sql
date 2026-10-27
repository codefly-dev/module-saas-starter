-- Admission keys return to one registration per key, unpinned, and proofs are
-- no longer remembered.
DROP TABLE public.hop_admission_proofs;
DROP INDEX public.hop_admission_keys_instance_key;
ALTER TABLE public.hop_admission_keys DROP CONSTRAINT hop_admission_keys_instance_check;
ALTER TABLE public.hop_admission_keys DROP COLUMN instance;
