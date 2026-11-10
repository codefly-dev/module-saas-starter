-- A role view fixes a session to act as some of the roles its person holds on
-- an installation. The record is the authority core's: written once when the
-- view is opened and never changed, so a session cannot be widened. A host
-- session row is its own; this keeps no foreign key, because the view is
-- written before the session it names and a missing session simply has no
-- credential to present.
CREATE TABLE public.authority_core_role_views (
    session_id text NOT NULL,
    parent_session_id text NOT NULL,
    person_id text NOT NULL,
    organization_id text NOT NULL,
    roles jsonb NOT NULL,
    CONSTRAINT authority_core_role_views_pkey PRIMARY KEY (session_id)
);
GRANT SELECT,INSERT ON TABLE public.authority_core_role_views TO app_control_plane;
