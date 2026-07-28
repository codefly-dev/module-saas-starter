CREATE TABLE public.analytics_deliveries (
    job_id UUID PRIMARY KEY REFERENCES public.job_messages(id) ON DELETE RESTRICT,
    command_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('event', 'suppression')),
    provider_reference TEXT NOT NULL CHECK (length(provider_reference) BETWEEN 1 AND 255),
    duplicate BOOLEAN NOT NULL DEFAULT FALSE,
    delivered_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX analytics_deliveries_command_idx
ON public.analytics_deliveries(command_id, delivered_at DESC);

REVOKE ALL PRIVILEGES ON public.analytics_deliveries
FROM PUBLIC, app_tenant, app_control_plane, app_billing_worker, app_webhook_worker, app_job_worker;

GRANT SELECT, INSERT ON public.analytics_deliveries TO app_job_worker;
GRANT UPDATE (provider_reference, duplicate, delivered_at)
ON public.analytics_deliveries TO app_job_worker;
