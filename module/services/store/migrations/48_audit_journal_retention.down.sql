REVOKE DELETE ON public.domain_events FROM app_control_plane;
DROP POLICY audit_journal_retention_delete ON public.domain_events;
REVOKE SELECT ON public.codefly_effect_receipts FROM app_control_plane;
DROP POLICY audit_delivery_retention_receipts ON public.codefly_effect_receipts;
