-- Dropping the tables removes their RLS policies, indexes, and grants. Leave the
-- ltree / btree_gist extensions in place — other objects may depend on them.
DROP TABLE IF EXISTS record_shares;
DROP TABLE IF EXISTS scope_grants;
DROP TABLE IF EXISTS scope_nodes;
