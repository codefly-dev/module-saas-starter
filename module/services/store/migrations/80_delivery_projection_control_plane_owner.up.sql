-- public.record_delivery_event is SECURITY DEFINER and was owned by the role
-- that applies this ledger. Its UPDATE of public.invitations is bound by that
-- table's FORCE ROW LEVEL SECURITY like any other statement, and no policy
-- there admits the ledger's owner: the projection advanced an invitation's
-- delivery status only where that owner is a superuser, which row-level
-- security does not bind. Where the store shares its server the owner is an
-- ordinary role, and the function recorded the event, matched no invitation
-- and reported success.
--
-- The function now runs as app_control_plane, the role every other
-- cross-tenant definer here runs as and the one the explicit-rows policy on
-- public.invitations admits. That role gains what the body needs and it lacked:
-- INSERT on the delivery ledger, and SELECT on the two columns of its replay
-- key, which ON CONFLICT (provider, event_id) reads. The job worker keeps
-- EXECUTE and still holds nothing on either table.
SET LOCAL lock_timeout = '15s';
GRANT INSERT, SELECT (provider, event_id) ON TABLE public.email_delivery_events TO app_control_plane;
GRANT CREATE ON SCHEMA public TO app_control_plane;
ALTER FUNCTION public.record_delivery_event(text,text,text,text,text,timestamp with time zone,uuid) OWNER TO app_control_plane;
REVOKE CREATE ON SCHEMA public FROM app_control_plane;
