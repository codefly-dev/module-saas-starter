-- The periodic reconcile covered GitHub and nothing else.
--
-- Two separate reasons, each sufficient on its own. A source of the pull
-- providers — the generic API, the crawler, object storage — was inserted with
-- next_reconcile_at NULL, because only the GitHub branch of AddSource ever set
-- it; and the due-set query filtered provider = 'github' on top of that. So a
-- connected api, crawler or upload source synced when a person pressed "Sync
-- now" and at no other time. None of the three has a webhook receiver either
-- (AddSource refuses a webhook secret for each), so there was no push path to
-- miss: the collection drifted from its source from the moment it was connected
-- and nothing ever noticed.
--
-- The host now schedules every provider and the sweep selects every provider,
-- routing each due row to the engine that provider syncs on. This backfills the
-- rows that already exist, which the code change alone cannot reach: they were
-- written before there was a schedule to write.
--
-- A day, not the 30 minutes the column defaults to for GitHub. These connectors
-- re-send their whole content as additions on every sync, with no cursor and no
-- deletions — the gap datasource_connectors.go registers against each of them —
-- so a 30-minute cadence would re-send an entire bucket or site 48 times a day.
-- Matches defaultDatasourcePullReconcileInterval in pkg/business/datasources.go;
-- the two are read together, so a change to one belongs with a change to the
-- other.
--
-- The first pass is spread across the first day rather than firing for every
-- backfilled source on the first sweep after deploy: a deployment with many pull
-- sources would otherwise start every one of their full re-sends within the same
-- minute. random() is per row.
--
-- Only 'active' rows are scheduled. A paused or degraded source is out of the
-- sweep by status anyway, and giving it a next_reconcile_at would have it rejoin
-- the moment an operator returned it to active without the operator asking.
--
-- datasource_sources FORCE ROW LEVEL SECURITY and its policies admit only the
-- request and control-plane roles, so the owner running this migration would
-- match zero rows through them. RLS is lifted for the statement and restored
-- immediately after, exactly as 2_drop_execution_custody does for its count.
ALTER TABLE public.datasource_sources NO FORCE ROW LEVEL SECURITY;

UPDATE public.datasource_sources
   SET reconcile_interval = INTERVAL '24 hours',
       next_reconcile_at  = NOW() + (random() * INTERVAL '24 hours'),
       updated_at         = NOW()
 WHERE provider <> 'github'
   AND status = 'active'
   AND next_reconcile_at IS NULL;

ALTER TABLE public.datasource_sources FORCE ROW LEVEL SECURITY;
