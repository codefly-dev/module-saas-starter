-- Public-client metadata grants no authority. Only the authority core's
-- control-plane transaction reads and replaces these identities.
CREATE TABLE public.authority_core_clients (
    id text PRIMARY KEY,
    name text NOT NULL,
    redirect_uris text[],
    revision numeric(20,0) NOT NULL CHECK (revision >= 0 AND revision <= 18446744073709551615),
    disabled boolean NOT NULL
);
GRANT SELECT,INSERT,UPDATE ON TABLE public.authority_core_clients TO app_control_plane;

-- Existing module delegations retain their grantee and acquire empty fields.
-- Validation of client versus module grantees belongs to the authority core.
ALTER TABLE public.authority_core_delegations
 ADD COLUMN client_id text NOT NULL DEFAULT '',
 ADD COLUMN audience text NOT NULL DEFAULT '',
 ADD COLUMN resource text NOT NULL DEFAULT '';
