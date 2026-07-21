-- Migration 14 originally created a Stripe-specific idempotency table. The
-- generic job platform supersedes it, but editing migration 14 cannot remove
-- the table from databases that already applied that migration. Converge those
-- existing installations explicitly; fresh databases treat this as a no-op.
DROP TABLE IF EXISTS public.stripe_webhook_events;
