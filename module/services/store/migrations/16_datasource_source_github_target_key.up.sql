-- A GitHub source could be connected twice: the same repository, branch, paths
-- and file types into the same collection, as two rows nothing told apart, each
-- syncing the same files into one collection (issue #978).
--
-- The host now refuses that in the connect transaction, under a per-repository
-- advisory lock, with a message naming the source already there. This index is
-- the same refusal at the table, so an insert path that skips that check — a
-- later one, or a change to the check — still cannot write the second row.
--
-- github_target_key is written by the host on insert
-- (business.GitHubSourceTargetKey: repository lower-cased, branch, sorted path
-- set, sorted extension set, boundary node). It is computed there rather than
-- here so the check and the index compare one value, not two encodings of it.
--
-- Rows that already exist keep a NULL key, which the partial index ignores.
-- Backfilling them would fail this migration on any deployment that already
-- holds a duplicate pair (the bug being fixed is how such pairs were made), and
-- choosing which of a pair to drop would discard a source's sync history and
-- delegations. The host's check still compares new connects against those rows.
ALTER TABLE public.datasource_sources ADD COLUMN github_target_key text;

CREATE UNIQUE INDEX datasource_sources_github_target_key
    ON public.datasource_sources USING btree (org_id, github_target_key)
 WHERE (github_target_key IS NOT NULL);
