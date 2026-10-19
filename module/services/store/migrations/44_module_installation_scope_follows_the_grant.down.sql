-- Irreversible: the scopes this migration reset were copies of each grant at
-- install time; nothing records them to restore. Following the grant is a
-- superset of every such snapshot.
SELECT 1;
