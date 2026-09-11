-- Reverse of 130: restore the pre-contract default publisher (the bare
-- solution id) on rows the up migration rewrote. Rows registered after the
-- upgrade also carry `solution:<id>` and are restored the same way, which is
-- what a pre-#540 gateway would have stored for them.
UPDATE solution_registrations
SET publisher = solution_id
WHERE publisher = 'solution:' || solution_id;
