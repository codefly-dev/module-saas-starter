-- Restore the historical shape only when rolling back the convergence
-- migration. Reapplying migration 77 removes it again.
CREATE TABLE IF NOT EXISTS public.stripe_webhook_events (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    received_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    processed_at TIMESTAMP WITH TIME ZONE,
    error        TEXT
);

CREATE INDEX IF NOT EXISTS idx_stripe_webhook_events_type
    ON public.stripe_webhook_events(type);
