-- Principal provenance (Identity Claims v1, ask #3): who created this principal.
-- NULL = a root principal (humans; pre-existing backfilled rows). For agents and
-- services this is the authorship root a policy-enforcement consumer (e.g. an AI gateway)
-- chains authority back to. ON DELETE SET NULL: deleting a creator never cascades
-- into the principals it created — provenance degrades to "root", it doesn't erase.

ALTER TABLE principals ADD COLUMN created_by UUID REFERENCES principals(id) ON DELETE SET NULL;
CREATE INDEX idx_principals_created_by ON principals(created_by);
