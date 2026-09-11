-- Solution registrations made before publisher-bound registration (#540)
-- carry the pre-contract default publisher: the bare solution id, which the
-- gateway filled in from the body when the caller named none. Since #540 the
-- publisher is the credential's verified subject, `solution:<id>`, and a write
-- whose publisher differs from the stored one is refused — so without this
-- rewrite every solution registered before the upgrade is locked out by its
-- own, now-verified, publisher (#613).
--
-- Only the default form is rewritten: a pre-contract row whose publisher was
-- self-asserted as something else was never authenticated, and deciding who
-- owns it is an operator's call, not a migration's.
UPDATE solution_registrations
SET publisher = 'solution:' || solution_id
WHERE publisher = solution_id;
