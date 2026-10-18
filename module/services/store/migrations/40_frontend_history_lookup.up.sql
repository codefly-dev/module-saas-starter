-- Before immutable payload preparation, history conferred no retained serving
-- identity. New applied decisions write the host-derived identity explicitly.
ALTER TABLE public.solution_generation_history ADD COLUMN asset_version text
    CHECK (asset_version IS NULL OR asset_version ~ '^[a-f0-9]{64}$');
CREATE UNIQUE INDEX solution_generation_frontend_version
    ON public.solution_generation_history(binding_id, asset_version)
    WHERE asset_version IS NOT NULL;
